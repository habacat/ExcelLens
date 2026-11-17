package checkpoint

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCheckpointChildProcess(t *testing.T) {
	mode := os.Getenv("EXCELLENS_CHECKPOINT_CHILD")
	if mode == "" {
		return
	}
	s, err := Open(os.Getenv("EXCELLENS_CHECKPOINT_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l, err := s.Acquire(context.Background(), os.Getenv("EXCELLENS_CHECKPOINT_ID"), os.Getenv("EXCELLENS_CHECKPOINT_BINDING"))
	if mode == "busy" {
		if !errors.Is(err, ErrBusy) {
			if l != nil {
				_ = l.Close()
			}
			t.Fatal("cross-process writer lock was bypassed", err)
		}
		fmt.Println("CHECKPOINT-BUSY")
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if mode == "commit-tail" {
		m := l.Snapshot()
		_, err := l.Advance(context.Background(), Advance{Revision: m.Revision, Stage: "scan", Cursor: json.RawMessage(`{"next":2}`), Records: 1, Bytes: 4}, strings.NewReader("two\n"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := l.data.Seek(0, io.SeekEnd); err != nil {
			t.Fatal(err)
		}
		if _, err := l.data.Write([]byte("unconfirmed partial frame")); err != nil {
			t.Fatal(err)
		}
		if err := l.data.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println("CHECKPOINT-READY")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func childCommand(t *testing.T, root string, m Manifest, mode string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestCheckpointChildProcess$", "-test.v")
	cmd.Env = append(os.Environ(), "EXCELLENS_CHECKPOINT_CHILD="+mode, "EXCELLENS_CHECKPOINT_ROOT="+root, "EXCELLENS_CHECKPOINT_ID="+m.ID, "EXCELLENS_CHECKPOINT_BINDING="+m.DefinitionHash)
	hideTestProcess(cmd)
	return cmd
}

func TestCheckpointRealProcessExclusionCrashAndConfirmedTailRecovery(t *testing.T) {
	s, def, _ := fixture(t)
	l := create(t, s, def)
	first := advance(t, l, `{"next":1}`, 1, []byte("one\n"), false)
	busy := childCommand(t, s.root.Name(), first, "busy")
	output, err := busy.CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("CHECKPOINT-BUSY")) {
		t.Fatal("real second process was admitted", string(output), err)
	}
	_ = l.Close()
	child := childCommand(t, s.root.Name(), first, "commit-tail")
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = child.Stdout
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	stop := sync.OnceFunc(func() {
		_ = child.Process.Kill() // only this test's own isolated helper process
		_ = stdin.Close()
		_ = child.Wait()
	})
	t.Cleanup(stop)
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if scanner.Text() == "CHECKPOINT-READY" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("helper exited without a confirmed second block")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("isolated checkpoint helper did not become ready")
	}
	if _, err := s.Acquire(context.Background(), first.ID, first.DefinitionHash); !errors.Is(err, ErrBusy) {
		t.Fatal("live child lost exclusive ownership", err)
	}
	if err := s.Discard(first.ID); !errors.Is(err, ErrBusy) {
		t.Fatal("live child checkpoint was discarded", err)
	}
	stop()
	resumed, err := s.Acquire(context.Background(), first.ID, first.DefinitionHash)
	if err != nil {
		t.Fatal("terminated process left an unrecoverable stale lock", err)
	}
	defer resumed.Close()
	m := resumed.Snapshot()
	if m.Records != 2 || m.Blocks != 2 || m.Revision != first.Revision+1 || string(storedBytes(t, resumed)) != "one\ntwo\n" {
		t.Fatal("process crash lost a confirmed record or accepted its unconfirmed tail", m)
	}
	info, _ := resumed.data.Stat()
	if info.Size() != m.DataBytes {
		t.Fatal("recovery did not trim exactly the unconfirmed tail")
	}
}
