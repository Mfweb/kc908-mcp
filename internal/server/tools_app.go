// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mfweb/ktos-mcp/internal/device"
	"github.com/mfweb/ktos-mcp/internal/transfer"
)

type AppUploadArgs struct {
	Filename   string `json:"filename,omitempty" jsonschema:"Target .kcapp filename under SD /apps/ (ASCII leaf only, e.g. sat_tracker.kcapp). Defaults to basename of local_path."`
	LocalPath  string `json:"local_path,omitempty" jsonschema:"Path to a .kcapp WebAssembly v1 module on the computer running MCP; choose this or data_base64"`
	DataBase64 string `json:"data_base64,omitempty" jsonschema:"Base64-encoded WebAssembly v1 module bytes; choose this or local_path"`
	Launch     bool   `json:"launch,omitempty" jsonschema:"Launch the uploaded .kcapp inside the standard device sandbox after verified commit"`
}

type AppNameArgs struct {
	Filename string `json:"filename" jsonschema:"Exact .kcapp leaf filename under SD /apps/ (no paths or traversal)"`
}

type AppConsoleArgs struct {
	Cursor uint64 `json:"cursor,omitempty" jsonschema:"Monotonic byte offset returned in next by the previous app_console call; start at 0"`
}

func addAppTools(s *mcp.Server, dev device.Device, transfers *transfer.Client) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "app_list",
		Description: "List installed .kcapp modules from the last SD /apps scan and report active sandbox state, last exit status, and debug console byte cursor.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, any, error) {
		return toolResult(dev.Call(ctx, "apps"))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "app_scan",
		Description: "Trigger a sandboxed SD /apps rescan on the device.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, any, error) {
		return toolResult(dev.Call(ctx, "app_scan"))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "app_upload",
		Description: "Upload a validated WebAssembly v1 .kcapp module (<= 4 MiB) into SD /apps/ over USB3 with SHA-256 verification and SD lease enforcement. Optionally launch inside the standard unprivileged KCApp sandbox.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a AppUploadArgs) (*mcp.CallToolResult, any, error) {
		data, filename, err := transfer.ReadAppBytes(transfer.AppInput{
			Filename:   a.Filename,
			LocalPath:  a.LocalPath,
			DataBase64: a.DataBase64,
		})
		if err != nil {
			return toolResult(nil, err)
		}

		res, err := transfers.UploadApp(ctx, filename, data)
		if err != nil {
			return toolResult(nil, err)
		}
		if a.Launch {
			if _, err = dev.Call(ctx, "app_launch "+filename); err != nil {
				return toolResult(nil, err)
			}
			res.Launched = true
		}

		raw, _ := json.Marshal(res)
		return toolResult(raw, nil)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "app_remove",
		Description: "Delete one exact regular .kcapp file under SD /apps/. Refuses symlinks, subdirectories, or files outside /apps/.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a AppNameArgs) (*mcp.CallToolResult, any, error) {
		if err := transfers.RemoveApp(ctx, a.Filename); err != nil {
			return toolResult(nil, err)
		}
		return toolResult(json.RawMessage(`{"ok":true}`), nil)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "app_launch",
		Description: "Launch an installed .kcapp inside the standard unprivileged WAMR AOT sandbox (UID 64010/64011, no_new_privs, RLIMIT_FSIZE=0). Refuses if RF/TX/recording/USB-storage is busy.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a AppNameArgs) (*mcp.CallToolResult, any, error) {
		if !transfer.ValidKCAppName(a.Filename) {
			return toolResult(nil, fmt.Errorf("invalid .kcapp filename"))
		}
		return toolResult(dev.Call(ctx, "app_launch "+a.Filename))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "app_stop",
		Description: "Stop the active foreground KCApp and release all claimed RF/audio/storage resources.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, any, error) {
		return toolResult(dev.Call(ctx, "app_stop"))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "app_console",
		Description: "Read incremental guest KC_CONSOLE debug output from the bounded 8 KiB ring buffer starting at cursor. Exposes ONLY guest KC_CONSOLE output, never system logs or shell.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a AppConsoleArgs) (*mcp.CallToolResult, any, error) {
		return toolResult(dev.Call(ctx, fmt.Sprintf("app_console %d", a.Cursor)))
	})
}
