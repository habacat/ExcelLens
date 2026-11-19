package main

import (
	"strconv"
	"testing"
)

func TestProductionImportBoundaries(t *testing.T) {
	for _, tc := range []struct {
		file, imported string
		allowed        bool
	}{
		{"internal/opc/rewrite.go", "archive/zip", true},
		{"internal/opc/rewrite.go", "golang.org/x/text/encoding", false},
		{"internal/editfile/layout.go", "excellens/internal/opc", true},
		{"internal/editfile/layout.go", "excellens/tools/icon", false},
		{"cmd/perfprobe/main.go", "excellens/internal/workbook", true},
		{"cmd/perfprobe/main.go", "excellens/internal/server", false},
	} {
		t.Run(tc.file+"/"+tc.imported, func(t *testing.T) {
			src := []byte("package p\nimport _ " + strconv.Quote(tc.imported))
			err := checkPackageImports(tc.file, src)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed = %t, got %v", tc.allowed, err)
			}
		})
	}
}
