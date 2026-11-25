// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mfweb/ktos-mcp/internal/transfer"
)

type ImageUploadArgs struct {
	Background bool   `json:"background,omitempty" jsonschema:"Start a background transfer and return its ID. Files over 128 KiB always use background transfer; query image_transfer_status until completed."`
	LocalPath  string `json:"local_path,omitempty" jsonschema:"Image path on the computer running MCP; choose this or data_base64"`
	DataBase64 string `json:"data_base64,omitempty" jsonschema:"Base64 encoded PNG JPEG or BMP bytes; choose this or local_path"`
}

type ImageDownloadArgs struct {
	DevicePath    string `json:"device_path,omitempty" jsonschema:"SD image path returned by image_upload, or @sstv (default) for a snapshot of the decoded SSTV image"`
	LocalPath     string `json:"local_path,omitempty" jsonschema:"Optional new output file on the MCP computer; existing files are never overwritten"`
	IncludeBase64 bool   `json:"include_base64,omitempty" jsonschema:"Also return exact file bytes as base64; normally prefer local_path and image preview"`
}

type ImageRemoveArgs struct {
	DevicePath string `json:"device_path" jsonschema:"Exact /mnt/sdcard/tmp/ktos-image-... path; only the temporary image cache can be deleted"`
}

type ImageTransferArgs struct {
	TransferID string `json:"transfer_id" jsonschema:"ID returned by background image_upload"`
	Cancel     bool   `json:"cancel,omitempty" jsonschema:"Request cancellation; poll until cancelled, failed or completed before starting another image transfer"`
}

func addImageTools(s *mcp.Server, transfers *transfer.Client) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "image_upload",
		Description: "Upload PNG/JPEG/BMP from a local file or base64 bytes into SD tmp. Over 128 KiB returns a background transfer_id: poll image_transfer_status until completed for the verified device_path, then set tx.file. Does not start transmission. Requires device-owned SD and System 1.0.9.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a ImageUploadArgs) (*mcp.CallToolResult, any, error) {
		data, err := transfer.ReadImageBytes(transfer.ImageInput{
			LocalPath:  a.LocalPath,
			DataBase64: a.DataBase64,
		})
		if err == nil && (a.Background || len(data) > transfer.BackgroundThreshold) {
			job, startErr := transfers.StartUpload(data)
			raw, _ := json.Marshal(job)
			return toolResult(raw, startErr)
		}

		var info transfer.ImageInfo
		if err == nil {
			info, err = transfers.UploadImage(ctx, data)
		}
		raw, _ := json.Marshal(info)
		return toolResult(raw, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "image_download",
		Description: "Download an SD image, or snapshot the current decoded SSTV image (@sstv). Verifies SHA-256, returns image preview, optionally saves a new local file or returns exact base64 bytes. Device-owned SD required.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a ImageDownloadArgs) (*mcp.CallToolResult, any, error) {
		info, data, err := transfers.DownloadImage(ctx, a.DevicePath)
		if err == nil && a.LocalPath != "" {
			err = transfer.SaveDownload(a.LocalPath, data)
			if err == nil {
				info.LocalPath, _ = filepath.Abs(a.LocalPath)
			}
		}
		if err == nil && a.IncludeBase64 {
			info.DataBase64 = base64.StdEncoding.EncodeToString(data)
		}

		raw, _ := json.Marshal(info)
		result, value, resErr := toolResult(raw, err)
		if err == nil && resErr == nil {
			preview, mime := transfer.ImagePreview(data, info.MIME)
			if len(preview) > 0 {
				result.Content = append(result.Content, &mcp.ImageContent{
					Data:     preview,
					MIMEType: mime,
				})
			}
		}
		return result, value, resErr
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "image_remove",
		Description: "Remove one exact KTOS temporary image cache file after use. Does not remove user images or other SD files.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a ImageRemoveArgs) (*mcp.CallToolResult, any, error) {
		if err := transfers.RemoveImage(ctx, a.DevicePath); err != nil {
			return toolResult(nil, err)
		}
		return toolResult(json.RawMessage(`{"ok":true}`), nil)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "image_transfer_status",
		Description: "Read a background image upload's progress and verified result; optionally request cancellation. Only state=completed provides a usable device_path. The latest job is retained until another upload starts; restarting MCP cancels unfinished transfers.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, a ImageTransferArgs) (*mcp.CallToolResult, any, error) {
		job, err := transfers.TransferStatus(a.TransferID, a.Cancel)
		raw, _ := json.Marshal(job)
		return toolResult(raw, err)
	})
}
