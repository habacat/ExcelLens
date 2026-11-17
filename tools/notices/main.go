// Generate distributable license notices from the pinned vendor tree.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	out := flag.String("o", "THIRD_PARTY_NOTICES.txt", "output file")
	flag.Parse()
	manifest, err := os.ReadFile("vendor/modules.txt")
	if err != nil {
		panic(err)
	}
	var result bytes.Buffer
	result.WriteString("ExcelLens third-party notices\n\nPinned pure-Go dependencies are compiled into the application.\nNo external archive program is required at runtime.\nExact source versions are recorded below and in vendor/modules.txt.\n\n")
	for _, line := range strings.Split(string(manifest), "\n") {
		if !strings.HasPrefix(line, "# ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		module, version := fields[1], fields[2]
		root := filepath.Join("vendor", filepath.FromSlash(module))
		var licenses []string
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			name := strings.ToUpper(d.Name())
			if strings.HasSuffix(name, ".GO") {
				return nil
			}
			for _, prefix := range []string{"LICENSE", "COPYING", "COPYRIGHT", "NOTICE"} {
				if strings.HasPrefix(name, prefix) {
					licenses = append(licenses, path)
					break
				}
			}
			return nil
		})
		if err != nil {
			panic(err)
		}
		if len(licenses) == 0 {
			panic("no license found for " + module)
		}
		sort.Strings(licenses)
		fmt.Fprintf(&result, "============================================================\n%s %s\n============================================================\n", module, version)
		for _, path := range licenses {
			raw, err := os.ReadFile(path)
			if err != nil {
				panic(err)
			}
			rel, _ := filepath.Rel(root, path)
			fmt.Fprintf(&result, "\n--- %s ---\n%s\n", filepath.ToSlash(rel), raw)
		}
		result.WriteString("\n")
	}
	if err := os.WriteFile(*out, result.Bytes(), 0644); err != nil {
		panic(err)
	}
	fmt.Printf("wrote %s (%d bytes)\n", *out, result.Len())
}
