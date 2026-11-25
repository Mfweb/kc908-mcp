// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mfweb/ktos-mcp/internal/device"
)

type GPSControlArgs struct {
	Operation        string `json:"operation" jsonschema:"start, stop, sync_location, sync_clock, track_save, or track_reset"`
	OpenPage         bool   `json:"open_page,omitempty" jsonschema:"When operation is start, also open the full-screen GPS L1 view on the instrument display"`
	TimezoneQuarters *int   `json:"timezone_quarters,omitempty" jsonschema:"Optional UTC offset in 15-minute increments (-48..56, e.g. 32 for UTC+08:00) when operation is sync_clock; omit for automatic lookup from GPS fix"`
}

type GPSTrackArgs struct {
	Offset uint32 `json:"offset,omitempty" jsonschema:"Zero-based track point offset for pagination"`
	Limit  uint32 `json:"limit,omitempty" jsonschema:"Maximum points per page (1..200; default 64)"`
}

func addGPSTools(s *mcp.Server, dev device.Device) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gps_control",
		Description: "Control software GPS L1 reception (1575.42 MHz): start/stop GPS L1, apply the current fix to device location, sync device RTC clock and timezone, export recorded GPX track to SD, or reset the in-memory track.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a GPSControlArgs) (*mcp.CallToolResult, any, error) {
		cmd, err := buildGPSControlCommand(a)
		if err != nil {
			return toolResult(nil, err)
		}
		return toolResult(dev.Call(ctx, cmd))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gps_status",
		Description: "Read live software GPS L1 status: 2D/3D fix coordinates, altitude, PDOP, UTC time, device location, tracked satellite table (PRN, C/N0, Doppler, azimuth, elevation, flags), and recorded track summary.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, any, error) {
		return toolResult(dev.Call(ctx, "gps"))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gps_track",
		Description: "Read paginated GPS L1 track points recorded in device memory.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a GPSTrackArgs) (*mcp.CallToolResult, any, error) {
		limit := a.Limit
		if limit == 0 {
			limit = 64
		}
		if limit > 200 {
			return toolResult(nil, fmt.Errorf("limit must be in 1..200"))
		}
		return toolResult(dev.Call(ctx, fmt.Sprintf("gps_track %d %d", a.Offset, limit)))
	})
}

func buildGPSControlCommand(a GPSControlArgs) (string, error) {
	switch a.Operation {
	case "start":
		if a.OpenPage {
			return "action gps_start 1", nil
		}
		return "action gps_start 0", nil
	case "stop":
		return "action gps_stop", nil
	case "sync_location":
		return "action gps_sync_location", nil
	case "sync_clock":
		if a.TimezoneQuarters == nil {
			return "action gps_sync_clock", nil
		}
		if *a.TimezoneQuarters < -48 || *a.TimezoneQuarters > 56 {
			return "", fmt.Errorf("timezone_quarters must be in -48..56")
		}
		return fmt.Sprintf("action gps_sync_clock %d", *a.TimezoneQuarters), nil
	case "track_save":
		return "action gps_track_save", nil
	case "track_reset":
		return "action gps_track_reset", nil
	default:
		return "", fmt.Errorf("unsupported GPS operation %q", a.Operation)
	}
}
