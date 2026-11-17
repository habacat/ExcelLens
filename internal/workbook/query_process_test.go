//go:build windows || linux || darwin || freebsd || openbsd || netbsd || dragonfly

package workbook

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQueryFileProcessOwner(t *testing.T) {
	if dir := os.Getenv("EXCELLENS_QUERY_PROCESS_TEST"); dir != "" {
		file, err := newQueryFile(dir, &queryQuota{max: 1024})
		if err == nil {
			_, err = file.Write([]byte("closed but still owned sort run"))
		}
		if err == nil {
			err = file.close()
		}
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println(filepath.Base(file.path))
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		return // deliberately leave the file, like an interrupted process
	}
	dir := filepath.Join(t.TempDir(), "queries")
	child := exec.Command(os.Args[0], "-test.run=^TestQueryFileProcessOwner$")
	child.Env = append(os.Environ(), "EXCELLENS_QUERY_PROCESS_TEST="+dir)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	child.Stderr = &stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close(); _ = child.Process.Kill(); _ = child.Wait() }()
	ready := make(chan string, 1)
	go func() { name, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- strings.TrimSpace(name) }()
	var name string
	select {
	case name = <-ready:
	case <-time.After(20 * time.Second):
		t.Fatal("query owner process did not become ready")
	}
	pid, owned := queryFileOwner(name)
	if !owned || !queryProcessAlive(pid) || !QueryFilesInUse(filepath.Dir(dir)) {
		t.Fatal("live closed run was not recognized", name)
	}
	quota := &queryQuota{max: 1024}
	if err := quota.cleanStale(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Fatal("another live process's closed run was deleted", err)
	}
	_, _ = stdin.Write([]byte("exit\n"))
	if err := child.Wait(); err != nil {
		t.Fatalf("owner process failed: %v / %s", err, stderr.String())
	}
	if queryProcessAlive(pid) || QueryFilesInUse(filepath.Dir(dir)) {
		t.Fatal("ended process still retained a query lease")
	}
	if err := quota.cleanStale(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Fatal("interrupted process file was not reclaimed", err)
	}
}
