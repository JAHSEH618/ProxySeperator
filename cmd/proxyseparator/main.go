package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	appassets "github.com/friedhelmliu/ProxySeperator"
	backendapp "github.com/friedhelmliu/ProxySeperator/internal/app"
	"github.com/friedhelmliu/ProxySeperator/internal/tunhelper"
	"github.com/wailsapp/wails/v3/pkg/application"
)

const singletonAddr = "127.0.0.1:17899"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "tun-helper" {
		if err := tunhelper.Run(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// Singleton check: bind a fixed port to prevent multiple instances.
	// If the port is already taken, signal the existing instance to show
	// its window and exit.  Set PROXYSEPARATOR_ALLOW_MULTI_INSTANCE=1 to
	// skip this check during development.
	var singletonLn net.Listener
	if os.Getenv("PROXYSEPARATOR_ALLOW_MULTI_INSTANCE") == "" {
		ln, err := net.Listen("tcp", singletonAddr)
		if err != nil {
			activateExistingInstance()
			return
		}
		singletonLn = ln
		defer ln.Close()
	}

	backend := backendapp.NewBackendAPI()

	app := application.New(application.Options{
		Name:        "ProxySeparator",
		Description: "公司流量走公司代理，其余流量走个人代理",
		Services: []application.Service{
			application.NewService(backend),
		},
		Assets: application.AssetOptions{
			Handler: application.BundledAssetFileServer(appassets.FS()),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
	})

	backend.BindEvents(func(name string, payload any) {
		app.EmitEvent(name, payload)
	})

	window := app.NewWebviewWindowWithOptions(application.WebviewWindowOptions{
		Title:  "ProxySeparator",
		Width:  980,
		Height: 720,
	})
	window.Center()

	backend.OnWindowRestore(func() {
		window.Show()
	})

	// Start accepting singleton connections to handle "show window"
	// requests from subsequent launches.
	if singletonLn != nil {
		go acceptSingletonConnections(singletonLn, window)
	}

	// Catch SIGTERM/SIGINT: restore network state before process dies.
	// This covers: kill <pid>, stop.sh, system shutdown, etc.
	// OnShutdown (Wails lifecycle) is a safety net but not guaranteed
	// to fire on external signals.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		// A second signal means "force quit now" — skip cleanup.
		go func() { <-sigCh; os.Exit(1) }()
		_ = backend.Shutdown(context.Background())
		os.Exit(0)
	}()

	tray := app.NewSystemTray()
	tray.SetLabel("ProxySeparator")
	tray.AttachWindow(window)
	tray.OnClick(func() {
		window.Show()
	})
	trayMenu := app.NewMenu()
	trayMenu.Add("Open Window").OnClick(func(ctx *application.Context) {
		window.Show()
	})
	trayMenu.Add("Start Isolation").OnClick(func(ctx *application.Context) {
		_, _ = backend.Start(context.Background())
	})
	trayMenu.Add("Stop Isolation").OnClick(func(ctx *application.Context) {
		_ = backend.Stop(context.Background())
	})
	trayMenu.AddSeparator()
	trayMenu.Add("Repair Network").OnClick(func(ctx *application.Context) {
		_ = backend.ForceRecoverNetwork()
	})
	trayMenu.Add("Quit").OnClick(func(ctx *application.Context) {
		_ = backend.Shutdown(context.Background())
		app.Quit()
	})
	tray.SetMenu(trayMenu)

	// Periodically update the tray label to reflect the runtime state.
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			status, _ := backend.GetRuntimeStatus(context.Background())
			switch status.State {
			case "running":
				mode := "System"
				if status.Mode == "tun" {
					mode = "TUN"
				}
				tray.SetLabel(fmt.Sprintf("PS - %s", mode))
			case "error":
				tray.SetLabel("PS - Error")
			default:
				tray.SetLabel("PS")
			}
		}
	}()

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// activateExistingInstance signals a running instance to show its window,
// then exits.
func activateExistingInstance() {
	conn, err := net.DialTimeout("tcp", singletonAddr, 2*time.Second)
	if err == nil {
		_, _ = conn.Write([]byte("SHOW\n"))
		_ = conn.Close()
	}
	fmt.Fprintln(os.Stderr, "ProxySeparator is already running.")
	os.Exit(0)
}

// acceptSingletonConnections listens for connections from duplicate launches
// and shows the window when they send "SHOW".
func acceptSingletonConnections(ln net.Listener, window *application.WebviewWindow) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 16)
		_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, _ := conn.Read(buf)
		_ = conn.Close()
		if strings.TrimSpace(string(buf[:n])) == "SHOW" {
			window.Show()
		}
	}
}
