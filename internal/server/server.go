// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

// Package server constructs the KTOS MCP server and registers all instrument,
// image-transfer, and KCApp tools.
package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mfweb/ktos-mcp/internal/device"
	"github.com/mfweb/ktos-mcp/internal/transfer"
)

// Instrument MCP tool input schemas.

type Empty struct{}

type PageArgs struct {
	Offset uint32 `json:"offset,omitempty" jsonschema:"Pagination cursor returned by the previous call; start at zero"`
}

type GetArgs struct {
	Name string `json:"name"`
}

type ConfigureArgs struct {
	Parameters []device.Parameter `json:"parameters" jsonschema:"Ordered parameters; applied individually with validation. Earlier changes remain if a later entry fails."`
}

type ModeArgs struct {
	Mode string `json:"mode" jsonschema:"receiver, spectrum, source, correlator, compass, system, or vsa"`
}

type ActionArgs struct {
	Action   string `json:"action" jsonschema:"Name from instrument_capabilities actions"`
	Argument int    `json:"argument,omitempty"`
}

type TelemetryArgs struct {
	Topic string `json:"topic" jsonschema:"Name from instrument_capabilities telemetry (including adsb, dtmb, drm, vsa, gps, apps)"`
}

type InputArgs struct {
	Kind  string `json:"kind" jsonschema:"soft, key, knob, click, or page"`
	Value int    `json:"value"`
	Delta int    `json:"delta,omitempty"`
}

type TextArgs struct {
	Field string `json:"field" jsonschema:"source_file, tx.callsign, tx.destination, tx.path, tx.information, tx.file, channel_add, channel_save:SLOT, channel_rename:SLOT"`
	Text  string `json:"text" jsonschema:"UTF-8 text or device SD path"`
}

type ChannelArgs struct {
	Operation string `json:"operation" jsonschema:"load or delete"`
	Slot      uint32 `json:"slot" jsonschema:"Zero-based channel index 0..999"`
	Bank      uint32 `json:"bank,omitempty" jsonschema:"VFO bank 0 or 1"`
}

var instrumentModes = map[string]int{
	"receiver":   0,
	"spectrum":   1,
	"source":     2,
	"correlator": 3,
	"compass":    4,
	"system":     5,
	"vsa":        6,
}

var allowedTelemetryTopics = map[string]struct{}{
	"ptt": {}, "status": {}, "source": {}, "transmit": {}, "spectrum": {},
	"receiver": {}, "rxdigital": {}, "rxrecord": {}, "rxdecoders": {}, "rxmarkers": {},
	"rxgain": {}, "subtone": {}, "audio": {}, "channels": {}, "system-settings": {},
	"attitude": {}, "remote": {}, "rxperf": {}, "files": {}, "rxpeaklist": {},
	"rxautoset": {}, "rxlevel": {}, "rxdetectors": {}, "rxmasks": {}, "rxdpx": {},
	"rxwaterfall": {}, "licenses": {}, "environment": {}, "settings": {},
	"adsb": {}, "dtmb": {}, "drm": {}, "vsa": {}, "gps": {}, "apps": {},
}

func toolResult(raw json.RawMessage, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
		}, nil, nil
	}

	var value any
	if err = json.Unmarshal(raw, &value); err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}},
	}, value, nil
}

// New constructs a configured KTOS MCP server without returning a cleanup hook.
func New(dev device.Device, version string) *mcp.Server {
	s, _ := NewWithCleanup(dev, version)
	return s
}

// NewWithCleanup constructs a configured KTOS MCP server and returns a cleanup
// function that cancels and drains any active background transfers on exit.
func NewWithCleanup(dev device.Device, version string) (*mcp.Server, func()) {
	s := mcp.NewServer(&mcp.Implementation{Name: "ktos", Version: version}, nil)

	imgDev, _ := dev.(device.ImageDevice)
	transfers := transfer.NewClient(imgDev)

	addImageTools(s, transfers)
	addAppTools(s, dev, transfers)
	addVSATools(s, dev)
	addGPSTools(s, dev)
	addDRMTools(s, dev)
	addInstrumentTools(s, dev)

	return s, transfers.Shutdown
}

func addInstrumentTools(s *mcp.Server, dev device.Device) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "instrument_capabilities",
		Description: "Discover KTOS modes, semantic actions, telemetry, and protocol limits.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, any, error) {
		return toolResult(dev.Call(ctx, "capabilities"))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "instrument_parameters",
		Description: "List named settings with live values, bounds and integer constraints. Follow next until done.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a PageArgs) (*mcp.CallToolResult, any, error) {
		return toolResult(dev.Call(ctx, fmt.Sprintf("parameters %d", a.Offset)))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "instrument_get",
		Description: "Read one named instrument parameter.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a GetArgs) (*mcp.CallToolResult, any, error) {
		if strings.ContainsAny(a.Name, " \t\r\n\x00") {
			return toolResult(nil, fmt.Errorf("invalid name"))
		}
		return toolResult(dev.Call(ctx, "get "+a.Name))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "instrument_mode",
		Description: "Select receiver, spectrum analyzer, signal source, correlator, compass, system, or VSA (vector signal analyzer). The correlator requires a supported dual-receiver model. Stop output before switching.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a ModeArgs) (*mcp.CallToolResult, any, error) {
		modeID, ok := instrumentModes[a.Mode]
		if !ok {
			return toolResult(nil, fmt.Errorf("unsupported mode"))
		}
		return toolResult(dev.Call(ctx, fmt.Sprintf("mode %d", modeID)))
	})

	configureTools := []struct{ name, description string }{
		{
			name:        "spectrum_configure",
			description: "Configure frequency FREQ, SPAN, RBW, REF, detector, traces, markers, gains, triggers, peaks and sweep settings. Discover exact names with instrument_parameters.",
		},
		{
			name:        "receiver_configure",
			description: "Configure RX_FREQ, RX_SPAN, RX_MOD_DEV, rx_demod, RX_VOLUME, gain/AGC/filters/squelch/subtones, digital demodulation, waterfall, DPX and PTT parameters.",
		},
		{
			name:        "source_configure",
			description: "Configure source.frequency/amplitude/port/modulation and modulation-specific parameters; tx.source selects internal/APRS/SSTV/microphone/SD audio.",
		},
		{
			name:        "instrument_configure",
			description: "Set any supported system, compass, or instrument parameter. Hardware settings report pending state through system-settings telemetry.",
		},
	}
	for _, entry := range configureTools {
		mcp.AddTool(s, &mcp.Tool{
			Name:        entry.name,
			Description: entry.description,
		}, func(ctx context.Context, _ *mcp.CallToolRequest, a ConfigureArgs) (*mcp.CallToolResult, any, error) {
			if len(a.Parameters) == 0 || len(a.Parameters) > 64 {
				return toolResult(nil, fmt.Errorf("provide 1..64 parameters"))
			}

			commands := make([]string, len(a.Parameters))
			for i, p := range a.Parameters {
				var err error
				commands[i], err = device.ParameterCommand(p)
				if err != nil {
					return toolResult(nil, err)
				}
			}

			for i, cmd := range commands {
				if _, err := dev.Call(ctx, cmd); err != nil {
					return toolResult(nil, fmt.Errorf("%d applied; %s: %w", i, a.Parameters[i].Name, err))
				}
			}
			return toolResult(json.RawMessage(fmt.Sprintf(`{"ok":true,"applied":%d}`, len(commands))), nil)
		})
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "instrument_action",
		Description: "Run/stop, PTT hold (argument milliseconds 0..10000; 0 releases), autoset, VFO selection, recording, APRS/SSTV enable/rate/history/detail/save. Run in signal-source mode can emit RF.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a ActionArgs) (*mcp.CallToolResult, any, error) {
		if strings.ContainsAny(a.Action, " \t\r\n\x00") {
			return toolResult(nil, fmt.Errorf("invalid action"))
		}
		return toolResult(dev.Call(ctx, fmt.Sprintf("action %s %d", a.Action, a.Argument)))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "spectrum_read",
		Description: "Read an immutable snapshot of the current 800-point calibrated spectrum, frequency axis in Hz, level in dBm, frame sequence. Works in spectrum and receiver modes.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, any, error) {
		return toolResult(dev.Call(ctx, "fft"))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "instrument_status",
		Description: "Read live receiver, source, audio, decoder, recording, marker, settings, ADS-B, DTMB, DRM, VSA, GPS, and KCApp state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a TelemetryArgs) (*mcp.CallToolResult, any, error) {
		if _, ok := allowedTelemetryTopics[a.Topic]; !ok {
			return toolResult(nil, fmt.Errorf("unknown telemetry topic"))
		}
		return toolResult(dev.Call(ctx, a.Topic))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "instrument_menu",
		Description: "Read current page and its five function labels. Use physical-equivalent inputs for every interactive sub-menu.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, any, error) {
		return toolResult(dev.Call(ctx, "menu"))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "instrument_input",
		Description: "Operate device UI: soft keys 1..5; physical key code (HOME 0x8008, AF etc); knob index 0..2 and delta; click uses value=x/delta=y; page uses catalog ID.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a InputArgs) (*mcp.CallToolResult, any, error) {
		cmd, err := inputCommand(a)
		if err != nil {
			return toolResult(nil, err)
		}
		return toolResult(dev.Call(ctx, cmd))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "instrument_text",
		Description: "Set a UTF-8 channel name, APRS message/callsign/route, or SD source path.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a TextArgs) (*mcp.CallToolResult, any, error) {
		if a.Field == "" || strings.ContainsAny(a.Field, " \t\r\n\x00") {
			return toolResult(nil, fmt.Errorf("invalid field"))
		}
		return toolResult(dev.Call(ctx, "text "+a.Field+" "+hex.EncodeToString([]byte(a.Text))))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "receiver_channel",
		Description: "Load or delete a channel in the custom receiver. To add, save or rename use instrument_text with channel_add, channel_save:SLOT or channel_rename:SLOT.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a ChannelArgs) (*mcp.CallToolResult, any, error) {
		if a.Slot > 999 || a.Bank > 1 || (a.Operation != "load" && a.Operation != "delete") {
			return toolResult(nil, fmt.Errorf("invalid channel operation"))
		}
		return toolResult(dev.Call(ctx, fmt.Sprintf("channel %s %d %d", a.Operation, a.Slot, a.Bank)))
	})
}

func inputCommand(a InputArgs) (string, error) {
	switch a.Kind {
	case "soft":
		if a.Value < 1 || a.Value > 5 {
			return "", fmt.Errorf("soft key range 1..5")
		}
		return fmt.Sprintf("soft %d", a.Value), nil
	case "key":
		if a.Value < 0x8001 || a.Value > 0x8065 {
			return "", fmt.Errorf("invalid key")
		}
		return fmt.Sprintf("key %x", a.Value), nil
	case "knob":
		if a.Value < 0 || a.Value > 2 || a.Delta < -1000 || a.Delta > 1000 {
			return "", fmt.Errorf("invalid knob")
		}
		return fmt.Sprintf("knob %d %d", a.Value, a.Delta), nil
	case "click":
		if a.Value < 0 || a.Value >= 800 || a.Delta < 0 || a.Delta >= 480 {
			return "", fmt.Errorf("click outside display")
		}
		return fmt.Sprintf("click %d %d", a.Value, a.Delta), nil
	case "page":
		if a.Value < 0 {
			return "", fmt.Errorf("invalid page")
		}
		return fmt.Sprintf("page %d", a.Value), nil
	default:
		return "", fmt.Errorf("unknown input kind")
	}
}
