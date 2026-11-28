package main

import (
	"fmt"
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// These dependencies are intentional. New packages require an ownership and
// dependency decision here, rather than silently growing a cross-layer import.
var allowedInternalImports = map[string][]string{
	"analysis":      {"valueconv", "sourceview", "formats"},
	"archives":      {"formats", "workbook"},
	"branding":      {},
	"browserlease":  {},
	"buildinfo":     {},
	"calculator":    {"analysis", "valueconv", "formats", "excelformula"},
	"conditional":   {"formats", "excelformula"},
	"excelformula":  {},
	"checkpoint":    {"formats", "filelock"},
	"clustering":    {"workbook", "formats"},
	"composition":   {"workbook", "sourceview", "formats"},
	"editdoc":       {"checkpoint", "formats"},
	"editfile":      {"opc", "workbook", "editdoc", "formats", "separated", "calculator"},
	"export":        {"workbook", "formats", "separated"},
	"formats":       {},
	"i18n":          {},
	"fileidentity":  {},
	"instance":      {},
	"filelock":      {},
	"localstore":    {"filelock", "formats", "i18n"},
	"onlinedb":      {"formats", "i18n"},
	"opc":           {},
	"printing":      {"workbook", "formats"},
	"recipe":        {"analysis", "sourceview"},
	"reconcile":     {"analysis", "workbook", "formats"},
	"recovery":      {},
	"rowindex":      {"formats", "sourceview", "excelformula"},
	"searchdsl":     {},
	"separated":     {},
	"searchresults": {"formats", "separated"},
	"searchservice": {"searchresults", "workbook", "checkpoint", "formats"},
	"savedconfig":   {},
	"transferjobs":  {"savedconfig", "localstore", "i18n"},
	"server":        {"searchresults", "searchservice", "savedconfig", "transferjobs", "localstore", "browserlease", "analysis", "archives", "calculator", "export", "printing", "clustering", "composition", "formats", "i18n", "onlinedb", "searchdsl", "ui", "workbook", "buildinfo", "valueconv", "recovery", "checkpoint", "reconcile", "recipe", "sourceview", "editdoc", "editfile", "taskmanager"},
	"shellmenu":     {"formats", "i18n"},
	"sourceview":    {"formats"},
	"styles":        {"formats", "excelformula"},
	"taskmanager":   {},
	"tray":          {"branding", "formats", "workbook", "buildinfo", "i18n"},
	"ui":            {"formats", "buildinfo", "i18n"},
	"valueconv":     {},
	"workbook":      {"conditional", "excelformula", "rowindex", "styles", "searchresults", "fileidentity", "formats", "localstore", "searchdsl", "sourceview"},
}

var allowedCommandImports = map[string][]string{
	"excellens": {"localstore", "buildinfo", "i18n", "instance", "server", "shellmenu", "tray", "workbook"},
	"perfprobe": {"workbook"},
}

func checkPackageImports(name string, data []byte) error {
	path := filepath.ToSlash(name)
	root, sub, ok := strings.Cut(path, "/")
	if !ok || (root != "internal" && root != "cmd") || strings.HasSuffix(path, "_test.go") {
		return nil
	}
	owner := filepath.ToSlash(filepath.Dir(sub))
	policy := allowedInternalImports
	if root == "cmd" {
		policy = allowedCommandImports
	}
	allowed, ok := policy[owner]
	if !ok {
		return fmt.Errorf("package %s/%s has no ownership/dependency rule in tools/project", root, owner)
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), name, data, parser.ImportsOnly)
	if err != nil {
		return err
	}
	for _, imp := range parsed.Imports {
		value, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return err
		}
		if value == "C" {
			return fmt.Errorf("%s introduces CGO into the production application", name)
		}
		if projectPath, local := strings.CutPrefix(value, "excellens/"); local {
			target, internal := strings.CutPrefix(projectPath, "internal/")
			if !internal || !slices.Contains(allowed, target) {
				return fmt.Errorf("%s crosses package boundary %s/%s -> %s", name, root, owner, projectPath)
			}
		} else if root == "internal" && owner == "opc" {
			pkg, err := build.Default.Import(value, "", build.FindOnly|build.IgnoreVendor)
			if err != nil || !pkg.Goroot {
				return fmt.Errorf("%s imports %s outside the Go standard library", name, value)
			}
		}
	}
	return nil
}
