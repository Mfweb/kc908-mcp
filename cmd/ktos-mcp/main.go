// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

// Command ktos-mcp runs the stdio Model Context Protocol (MCP) server for the
// KC908 KTOS firmware, and provides one-shot diagnostic and KCApp CLI sub-modes
// over USB 3.0.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mfweb/ktos-mcp/internal/device"
	"github.com/mfweb/ktos-mcp/internal/server"
	"github.com/mfweb/ktos-mcp/internal/transfer"
)

func main() {
	os.Exit(run())
}

func run() int {
	exe, _ := os.Executable()
	defaultSDK := filepath.Join(filepath.Dir(exe), device.DefaultSDKName())

	sdkPath := flag.String("sdk", defaultSDK, fmt.Sprintf("Path to KTOS SDK (%s)", device.DefaultSDKName()))
	deviceIndex := flag.Uint("device", 0, "FT601 device index")
	command := flag.String("command", "", "Execute one SDK command and exit (diagnostic CLI)")
	appUpload := flag.String("app-upload", "", "Upload a local .kcapp module over USB3")
	appName := flag.String("app-name", "", "Target .kcapp filename on SD /apps/ (default: basename of -app-upload)")
	appRun := flag.Bool("app-run", false, "Launch -app-name (or uploaded app) in the device KCApp sandbox")
	appTail := flag.Bool("app-tail", false, "Stream guest KC_CONSOLE debug output until app exits or Ctrl-C")
	appStop := flag.Bool("app-stop", false, "Stop any active foreground KCApp and exit")
	flag.Parse()

	dev := device.NewLocked(*sdkPath, uint32(*deviceIndex))
	defer dev.Close()

	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()

	if *appStop {
		resp, err := dev.Call(ctx, "app_stop")
		if err != nil {
			log.Print(err)
			return 1
		}
		fmt.Println(string(resp))
		return 0
	}

	if *appUpload != "" || *appRun || *appTail {
		return runAppCLI(ctx, dev, *appUpload, *appName, *appRun, *appTail)
	}

	if *command != "" {
		resp, err := dev.Call(ctx, *command)
		if err != nil {
			log.Print(err)
			return 1
		}
		fmt.Println(string(resp))
		return 0
	}

	srv, cleanup := server.NewWithCleanup(dev, version)
	defer cleanup()

	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		log.Print(err)
		return 1
	}
	return 0
}

func runAppCLI(ctx context.Context, dev *device.LockedDevice, uploadPath, targetName string, launch, tail bool) int {
	transfers := transfer.NewClient(dev)
	target := targetName

	if uploadPath != "" {
		data, filename, err := transfer.ReadAppBytes(transfer.AppInput{
			Filename:  target,
			LocalPath: uploadPath,
		})
		if err != nil {
			log.Print(err)
			return 1
		}
		target = filename

		res, err := transfers.UploadApp(ctx, filename, data)
		if err != nil {
			log.Print(err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "Uploaded %s (%d bytes, sha256=%s)\n", res.DevicePath, res.Bytes, res.SHA256)
	}

	if launch {
		if !transfer.ValidKCAppName(target) {
			log.Print("provide -app-name or -app-upload with a valid .kcapp filename")
			return 1
		}
		if _, err := dev.Call(ctx, "app_launch "+target); err != nil {
			log.Print(err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "Launched %s\n", target)
	}

	if tail {
		if err := tailAppConsole(ctx, dev); err != nil {
			log.Print(err)
			return 1
		}
	}
	return 0
}

func tailAppConsole(ctx context.Context, dev device.Device) error {
	var cursor uint64
	sawActive := false

	for ctx.Err() == nil {
		raw, err := dev.Call(ctx, fmt.Sprintf("app_console %d", cursor))
		if err != nil {
			return err
		}

		var page struct {
			State   string `json:"state"`
			Next    uint64 `json:"next"`
			Dropped uint64 `json:"dropped"`
			Text    string `json:"text"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}

		if page.Dropped > 0 {
			fmt.Fprintf(os.Stderr, "[... %d console bytes dropped ...]\n", page.Dropped)
		}
		if page.Text != "" {
			fmt.Print(page.Text)
		}

		cursor = page.Next
		if page.State != "idle" {
			sawActive = true
		} else if sawActive {
			break
		}

		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}
