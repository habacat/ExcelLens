// project provides the same offline quality gate for local development and CI.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"excellens/internal/buildinfo"
	"excellens/internal/ui"
)

type step struct {
	Name       string `json:"name"`
	Result     string `json:"result"`
	DurationMS int64  `json:"durationMs"`
}

type receipt struct {
	Version string `json:"version"`
	OS      string `json:"os"`
	Go      string `json:"go"`
	Result  string `json:"result"`
	Steps   []step `json:"steps"`
}

var goVersionRE = regexp.MustCompile(`(?m)^go ([0-9.]+)\s*$`)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: go run ./tools/project {check|fmt|generate|version} [options]")
	}
	if args[0] == "version" {
		fmt.Println(buildinfo.Version)
		return nil
	}
	if args[0] == "render-index" {
		page, err := ui.Index("__APP_TOKEN__")
		if err == nil {
			_, err = os.Stdout.Write(page)
		}
		return err
	}
	if _, err := os.Stat("go.mod"); err != nil {
		return errors.New("run this command from the ExcelLens project root")
	}
	files, err := sourceFiles()
	if err != nil {
		return err
	}
	goExe := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goExe += ".exe"
	}
	env := commandEnv(false)
	if args[0] == "fmt" {
		for _, name := range files {
			if filepath.Ext(name) != ".go" {
				continue
			}
			data, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			formatted, err := format.Source(data)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if !bytes.Equal(data, formatted) {
				if err := os.WriteFile(name, formatted, 0644); err != nil {
					return err
				}
				fmt.Println(name)
			}
		}
		return nil
	}
	if args[0] == "generate" {
		if err := command(goExe, env, "run", "./tools/formulacatalog", "docs/user/functions.md"); err != nil {
			return err
		}
		return command(goExe, env, "run", "./tools/notices")
	}
	if args[0] != "check" {
		return fmt.Errorf("unknown project command %q", args[0])
	}
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	race := flags.Bool("race", false, "also run the Go race detector (requires a C compiler)")
	reportPath := flags.String("report", "", "write a JSON check receipt")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments after check options")
	}
	r := receipt{Version: buildinfo.Version, OS: runtime.GOOS, Go: runtime.Version(), Result: "PASS"}
	check := func(name string, fn func() error) error {
		fmt.Println("==>", name)
		start := time.Now()
		err := fn()
		result := "PASS"
		if err != nil {
			result, r.Result = "FAIL", "FAIL"
		}
		r.Steps = append(r.Steps, step{name, result, time.Since(start).Milliseconds()})
		return err
	}
	checks := []struct {
		name string
		fn   func() error
	}{
		{"project metadata, documentation and package boundaries", func() error { return audit(files) }},
		{"offline vendored dependency resolution", func() error { return quietCommand(goExe, env, "list", "-deps", "./...") }},
		{"Go tests", func() error { return command(goExe, env, "test", "-count=1", "./...") }},
		{"Go vet", func() error { return command(goExe, env, "vet", "./...") }},
		{"JavaScript syntax and regression tests", func() error { return checkJS(files, env) }},
		{"generated function catalog", func() error {
			cmd := exec.Command(goExe, "run", "./tools/formulacatalog")
			cmd.Env = env
			data, err := cmd.Output()
			if err != nil {
				return err
			}
			current, err := os.ReadFile("docs/user/functions.md")
			if err != nil {
				return err
			}
			if !bytes.Equal(bytes.ReplaceAll(current, []byte("\r\n"), []byte("\n")), data) {
				return errors.New("function catalog is stale; run go run ./tools/project generate")
			}
			return nil
		}},
	}
	if *race {
		checks = append(checks, struct {
			name string
			fn   func() error
		}{"Go race detector", func() error { return command(goExe, commandEnv(true), "test", "-race", "-count=1", "./...") }})
	}
	var checkErr error
	for _, c := range checks {
		if checkErr = check(c.name, c.fn); checkErr != nil {
			break
		}
	}
	if *reportPath != "" {
		if err := os.MkdirAll(filepath.Dir(*reportPath), 0755); err != nil {
			return errors.Join(checkErr, err)
		}
		data, err := json.MarshalIndent(r, "", "  ")
		if err == nil {
			err = os.WriteFile(*reportPath, append(data, '\n'), 0644)
		}
		checkErr = errors.Join(checkErr, err)
	}
	if checkErr == nil {
		fmt.Printf("PASS: ExcelLens %s project checks\n", buildinfo.Version)
	}
	return checkErr
}

func sourceFiles() ([]string, error) {
	var files []string
	for _, root := range []string{"cmd", "internal", "tools", "docs"} {
		err := filepath.WalkDir(root, func(name string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				files = append(files, name)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for _, name := range []string{"README.md", "AGENTS.md", "CONTRIBUTING.md", "NOTICE.md"} {
		files = append(files, name)
	}
	return files, nil
}

func commandEnv(race bool) []string {
	overrides := map[string]string{"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOFLAGS": "-mod=vendor", "CGO_ENABLED": "0"}
	if race {
		overrides["CGO_ENABLED"] = "1"
	}
	var env []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if _, replace := overrides[strings.ToUpper(key)]; !replace && !strings.EqualFold(key, "GOOS") && !strings.EqualFold(key, "GOARCH") {
			env = append(env, item)
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func command(exe string, env []string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", filepath.Base(exe), strings.Join(args, " "), err)
	}
	return nil
}

func quietCommand(exe string, env []string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w\n%s", strings.Join(args, " "), err, output)
	}
	return nil
}

func audit(files []string) error {
	mod, err := os.ReadFile("go.mod")
	if err != nil {
		return err
	}
	match := goVersionRE.FindSubmatch(mod)
	if len(match) != 2 || runtime.Version() != "go"+string(match[1]) {
		return fmt.Errorf("compiler %s must match go.mod exactly", runtime.Version())
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(buildinfo.Version) {
		return errors.New("release version must be major.minor.patch")
	}
	readme, err := os.ReadFile("README.md")
	if err != nil || !bytes.HasPrefix(readme, []byte("# ExcelLens "+buildinfo.Version+"\n")) {
		return errors.New("README title must match internal/buildinfo.Version")
	}
	for _, name := range files {
		if filepath.Ext(name) == ".md" {
			if err := checkLinks(name); err != nil {
				return err
			}
		}
		if filepath.Ext(name) != ".go" {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		formatted, err := format.Source(data)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if !bytes.Equal(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")), formatted) {
			return fmt.Errorf("%s needs gofmt; run go run ./tools/project fmt", name)
		}
		if err := checkPackageImports(name, data); err != nil {
			return err
		}
	}
	return checkReleaseFiles()
}

func checkLinks(name string) error {
	data, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	for _, m := range regexp.MustCompile(`\]\(([^)]+)\)`).FindAllSubmatch(data, -1) {
		link := strings.Trim(string(m[1]), "<>")
		if strings.Contains(link, ":") || strings.HasPrefix(link, "#") {
			continue
		}
		link, _, _ = strings.Cut(link, "#")
		if _, err := os.Stat(filepath.Join(filepath.Dir(name), filepath.FromSlash(link))); err != nil {
			return fmt.Errorf("%s: broken local link %q", name, link)
		}
	}
	return nil
}

func checkReleaseFiles() error {
	data, err := os.ReadFile("tools/release-files.json")
	if err != nil {
		return err
	}
	var files []string
	if err := json.Unmarshal(data, &files); err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, template := range files {
		name := strings.ReplaceAll(template, "{version}", buildinfo.Version)
		if filepath.IsAbs(name) || strings.Contains(name, "..") || strings.Contains(name, "\\") || seen[name] || strings.Contains(name, "/private/") {
			return fmt.Errorf("invalid or duplicate release path %q", name)
		}
		seen[name] = true
		if info, err := os.Stat(filepath.FromSlash(name)); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("release file missing or not regular: %s", name)
		}
	}
	if !seen["docs/releases/"+buildinfo.Version+".md"] {
		return errors.New("release manifest must include the current verification report")
	}
	var toolchain struct {
		Version string `json:"version"`
		SHA256  string `json:"windowsAMD64SHA256"`
	}
	data, err = os.ReadFile("tools/go-toolchain.json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &toolchain); err != nil {
		return err
	}
	if "go"+toolchain.Version != runtime.Version() || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(toolchain.SHA256) {
		return errors.New("toolchain download receipt must match go.mod and include its official SHA-256")
	}
	return nil
}

func checkJS(files, env []string) error {
	node, err := exec.LookPath("node")
	if err != nil {
		return errors.New("Node.js 22+ is required for development checks; the released app does not need Node")
	}
	version, err := exec.Command(node, "--version").Output()
	if err != nil {
		return err
	}
	major, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(string(version)), "v"), ".")
	n, err := strconv.Atoi(major)
	if err != nil || n < 22 {
		return fmt.Errorf("Node.js 22+ required, found %s", strings.TrimSpace(string(version)))
	}
	var tests []string
	for _, name := range files {
		ext := filepath.Ext(name)
		if ext != ".js" && ext != ".cjs" {
			continue
		}
		if err := quietCommand(node, env, "--check", name); err != nil {
			return err
		}
		if strings.HasSuffix(name, ".test.cjs") || strings.HasSuffix(name, "_test.cjs") {
			tests = append(tests, name)
		}
	}
	if len(tests) == 0 {
		return errors.New("no JavaScript regression tests found")
	}
	return command(node, env, append([]string{"--test", "--test-reporter=spec"}, tests...)...)
}
