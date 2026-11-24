package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"excellens/internal/buildinfo"
	"excellens/internal/instance"
	"excellens/internal/localstore"
	"excellens/internal/server"
	"excellens/internal/shellmenu"
	"excellens/internal/tray"
	"excellens/internal/workbook"
)

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "-version") {
		fmt.Println("ExcelLens", buildinfo.Version)
		return
	}
	// Acquire the mutex BEFORE constructing server/tray resources. Two launches
	// racing while the first tray window initializes must not create duplicates.
	owner, release, lockErr := instance.Acquire()
	if lockErr != nil {
		log.Printf("ExcelLens 单实例检查失败：%v", lockErr)
		return // fail closed: never start an unprotected duplicate process
	}
	if len(os.Args) > 1 && os.Args[1] == "--restore-data" {
		withPassword := len(os.Args) == 4 && os.Args[3] == "--password-stdin"
		if len(os.Args) != 3 && !withPassword {
			log.Fatal("用法：ExcelLens --restore-data 备份目录 [--password-stdin]")
		}
		if !owner {
			log.Fatal("请先退出目标电脑上的 ExcelLens 再迁入备份")
		}
		defer release()
		var options localstore.TransferOptions
		if withPassword {
			data, err := io.ReadAll(io.LimitReader(os.Stdin, 16385))
			if err != nil || len(data) > 16384 {
				log.Fatal("无法读取口令")
			}
			options.Password = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
			clear(data)
			if options.Password == "" {
				log.Fatal("缺少口令")
			}
		}
		config, err := localstore.ConfigDir()
		if err == nil {
			err = (localstore.Roots{Local: workbook.DataRoot(), Config: config}).RestoreWith(context.Background(), os.Args[2], options)
		}
		if err != nil {
			log.Fatal("迁入失败：", err)
		}
		fmt.Println("已核验并迁入本地备份")
		return
	}
	if !owner {
		var filePath string
		if len(os.Args) > 1 {
			if absolute, err := filepath.Abs(strings.Trim(os.Args[1], `"`)); err == nil {
				filePath = absolute
			}
		}
		// The primary process may own the mutex but not yet have its tray
		// message window. Retry briefly, then exit even if forwarding fails.
		for attempt := 0; attempt < 35; attempt++ {
			forwarded := false
			if filePath != "" {
				forwarded = tray.ForwardWorkbook(filePath)
			} else {
				forwarded = tray.ForwardOpen()
			}
			if forwarded {
				return
			}
			time.Sleep(120 * time.Millisecond)
		}
		return
	}
	defer release()
	closeLog := server.ConfigureLog()
	defer closeLog()

	app, err := server.New()
	if err != nil {
		log.Fatalf("初始化失败：%v", err)
	}
	defer app.Close()

	launchID := ""
	if len(os.Args) > 1 {
		filePath := strings.Trim(os.Args[1], `"`)
		if absolute, absErr := filepath.Abs(filePath); absErr == nil {
			var openErr error
			launchID, openErr = app.PreparePathForLaunch(absolute)
			if openErr != nil {
				log.Printf("打开启动参数中的工作簿失败：%v", openErr)
			}
		}
	}

	listener, url, err := app.Listen()
	if err != nil {
		log.Fatalf("启动失败：%v", err)
	}
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		if serveErr := app.Serve(listener); serveErr != nil {
			log.Printf("本机服务异常退出：%v", serveErr)
		}
	}()
	browserURL := url
	if launchID != "" {
		browserURL += "?session=" + launchID
	}
	if err := server.OpenBrowser(browserURL); err != nil {
		log.Printf("无法自动打开浏览器，请手动访问 %s：%v", url, err)
	}
	log.Printf("本机查看器已启动：%s", url)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-serveDone:
		case <-signals:
			app.Shutdown()
		}
		cancel()
	}()
	openViewer := func() {
		if openErr := server.OpenBrowser(url); openErr != nil {
			log.Printf("打开网页失败：%v", openErr)
		}
	}
	openDirectory := func(path string) error {
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
		return exec.Command("explorer.exe", path).Start()
	}
	openSettingDirectory := func(kind string) error {
		root, err := localstore.ConfigDir()
		if err != nil {
			return err
		}
		return openDirectory(filepath.Join(root, kind))
	}
	options := tray.Options{
		Address:          url,
		OnOpen:           openViewer,
		OnOpenProgram:    func() error { return openProgramDirectory(openDirectory) },
		RibbonLayout:     app.RibbonLayout,
		SetRibbonLayout:  app.SetRibbonLayout,
		OnClearAllCaches: app.ClearAllCaches,
		OnChooseFile: func(path string) error {
			id, err := app.PreparePathForLaunch(path)
			if err != nil {
				return err
			}
			return server.OpenBrowser(url + "?session=" + id)
		},
		OnOpenCache:          func() error { return openDirectory(server.CacheDir()) },
		OnOpenTemp:           func() error { return openDirectory(server.TempDir()) },
		OnOpenLog:            func() error { return openDirectory(server.LogDir()) },
		OnOpenExports:        func() error { return openDirectory(server.ExportDir()) },
		OnOpenNotes:          func() error { return openSettingDirectory("annotations") },
		OnOpenLayouts:        func() error { return openSettingDirectory("layouts") },
		OnClearCache:         app.ClearUnusedCaches,
		OnClearTemp:          app.ClearUnusedUploads,
		ContextMenuInstalled: shellmenu.IsInstalled,
		OnInstallContextMenu: shellmenu.Install,
		OnRemoveContextMenu:  shellmenu.Uninstall,
	}
	if err := tray.Run(ctx, options); err != nil {
		log.Printf("通知区域初始化失败：%v；仍可通过网页退出程序", err)
		<-ctx.Done()
	}
	app.Shutdown()
	<-serveDone
}

func openProgramDirectory(open func(string) error) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return open(filepath.Dir(exe))
}
