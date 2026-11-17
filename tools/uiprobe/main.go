// uiprobe serves the real application for headless developer checks, without
// a tray, browser launch, singleton mutex, or access to the user's local data.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"excellens/internal/server"
)

func main() {
	duration := flag.Duration("duration", 5*time.Minute, "maximum server lifetime")
	storage := flag.String("storage", "", "reuse an excellens-ui-qa-* directory directly under the OS temp directory")
	flag.Parse()
	var root string
	var err error
	if *storage == "" {
		root, err = os.MkdirTemp("", "excellens-ui-qa-*")
	} else {
		root, err = filepath.Abs(*storage)
		if err == nil && (filepath.Dir(root) != filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(root), "excellens-ui-qa-")) {
			log.Fatal("QA storage must be an isolated excellens-ui-qa-* directory directly under the OS temp directory")
		}
		if err == nil {
			err = os.MkdirAll(root, 0700)
		}
	}
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if *storage == "" && filepath.Dir(root) == filepath.Clean(os.TempDir()) {
			_ = os.RemoveAll(root)
		}
	}()
	for _, name := range []string{"LOCALAPPDATA", "APPDATA", "XDG_CACHE_HOME", "XDG_CONFIG_HOME"} {
		if err := os.Setenv(name, root); err != nil {
			log.Fatal(err)
		}
	}
	app, err := server.New()
	if err != nil {
		log.Fatal(err)
	}
	defer app.Close()
	listener, address, err := app.Listen()
	if err != nil {
		log.Fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]string{"url": address}); err != nil {
		log.Fatal(err)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		select {
		case <-signals:
		case <-time.After(*duration):
		}
		app.Shutdown()
	}()
	if err := app.Serve(listener); err != nil {
		log.Fatal(err)
	}
}
