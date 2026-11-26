// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mfweb/ktos-mcp/internal/device"
)

var drmProfiles = map[string]int{
	"auto":      0,
	"drm30_10k": 1,
	"drm30-10k": 1,
	"10k":       1,
	"10khz":     1,
	"drm30_20k": 2,
	"drm30-20k": 2,
	"20k":       2,
	"20khz":     2,
	"drmp_100k": 3,
	"drmp-100k": 3,
	"drm+":      3,
	"drmp":      3,
	"100k":      3,
	"100khz":    3,
}

var drmIQInvertModes = map[string]int{
	"auto":     0,
	"normal":   1,
	"off":      1,
	"invert":   2,
	"inverted": 2,
	"on":       2,
}

type DRMConfigureArgs struct {
	Start        bool     `json:"start,omitempty" jsonschema:"Enter DRM demodulation mode before applying settings"`
	OpenPage     bool     `json:"open_page,omitempty" jsonschema:"When start is true, also open the full-screen DRM details view on the instrument display"`
	FrequencyHz  *float64 `json:"frequency_hz,omitempty" jsonschema:"Receiver center frequency in Hz (RX_FREQ)"`
	Profile      string   `json:"profile,omitempty" jsonschema:"DRM profile: auto, drm30_10k, drm30_20k, or drmp_100k"`
	ServiceIndex *int     `json:"service_index,omitempty" jsonschema:"Zero-based DRM service index (0..3)"`
	Stereo       *bool    `json:"stereo,omitempty" jsonschema:"Enable or disable stereo audio decoding"`
	IQInvert     string   `json:"iq_invert,omitempty" jsonschema:"IQ spectrum inversion mode: auto, normal, or invert"`
}

type DRMControlArgs struct {
	Operation    string `json:"operation" jsonschema:"start, stop, reset, service, profile, stereo, or iq_invert"`
	OpenPage     bool   `json:"open_page,omitempty" jsonschema:"When operation is start, also open the full-screen DRM details view on the instrument display"`
	ServiceIndex *int   `json:"service_index,omitempty" jsonschema:"Zero-based DRM service index (0..3) when operation is service"`
	Profile      string `json:"profile,omitempty" jsonschema:"DRM profile (auto, drm30_10k, drm30_20k, or drmp_100k) when operation is profile"`
	Stereo       *bool  `json:"stereo,omitempty" jsonschema:"Enable or disable stereo audio decoding when operation is stereo"`
	IQInvert     string `json:"iq_invert,omitempty" jsonschema:"IQ spectrum inversion mode (auto, normal, or invert) when operation is iq_invert"`
}

func addDRMTools(s *mcp.Server, dev device.Device) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "drm_configure",
		Description: "Configure Digital Radio Mondiale (DRM30 / DRM+) reception: optionally start DRM, tune receiver frequency, choose profile (auto/drm30_10k/drm30_20k/drmp_100k), select service (0..3), toggle stereo, or set IQ spectrum inversion.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a DRMConfigureArgs) (*mcp.CallToolResult, any, error) {
		commands, err := buildDRMConfigureCommands(a)
		if err != nil {
			return toolResult(nil, err)
		}
		if len(commands) == 0 {
			return toolResult(nil, fmt.Errorf("provide at least one DRM configuration field"))
		}

		for i, cmd := range commands {
			if _, err := dev.Call(ctx, cmd); err != nil {
				return toolResult(nil, fmt.Errorf("%d applied; %s: %w", i, cmd, err))
			}
		}
		return toolResult(json.RawMessage(fmt.Sprintf(`{"ok":true,"applied":%d}`, len(commands))), nil)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "drm_control",
		Description: "Control DRM30 / DRM+ demodulation: start/stop DRM mode, reset demodulator lock, switch active service (0..3), change profile, toggle stereo, or change IQ inversion.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a DRMControlArgs) (*mcp.CallToolResult, any, error) {
		cmd, err := buildDRMControlCommand(a)
		if err != nil {
			return toolResult(nil, err)
		}
		return toolResult(dev.Call(ctx, cmd))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "drm_status",
		Description: "Read live DRM30 / DRM+ demodulator status: time/frame/FAC/SDC/MSC/audio lock flags, robustness mode, spectrum occupancy, QAM & protection levels, SNR, Doppler, CFO, SRO, audio codec/bitrate/underruns, FAC/SDC/MSC counters, services list, and text message.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, any, error) {
		return toolResult(dev.Call(ctx, "drm"))
	})
}

func parseDRMProfile(raw string) (int, error) {
	id, ok := drmProfiles[strings.ToLower(strings.TrimSpace(raw))]
	if !ok {
		return 0, fmt.Errorf("unsupported DRM profile %q (expected auto, drm30_10k, drm30_20k, or drmp_100k)", raw)
	}
	return id, nil
}

func parseDRMIQInvert(raw string) (int, error) {
	id, ok := drmIQInvertModes[strings.ToLower(strings.TrimSpace(raw))]
	if !ok {
		return 0, fmt.Errorf("unsupported DRM iq_invert %q (expected auto, normal, or invert)", raw)
	}
	return id, nil
}

func buildDRMConfigureCommands(a DRMConfigureArgs) ([]string, error) {
	var commands []string

	if a.Start {
		if a.OpenPage {
			commands = append(commands, "action drm_start 1")
		} else {
			commands = append(commands, "action drm_start 0")
		}
	}
	if a.FrequencyHz != nil {
		if *a.FrequencyHz <= 0 {
			return nil, fmt.Errorf("DRM frequency_hz must be positive")
		}
		cmd, err := device.ParameterCommand(device.Parameter{Name: "RX_FREQ", Value: *a.FrequencyHz})
		if err != nil {
			return nil, err
		}
		commands = append(commands, cmd)
	}
	if a.Profile != "" {
		id, err := parseDRMProfile(a.Profile)
		if err != nil {
			return nil, err
		}
		commands = append(commands, fmt.Sprintf("action drm_profile %d", id))
	}
	if a.ServiceIndex != nil {
		if *a.ServiceIndex < 0 || *a.ServiceIndex > 3 {
			return nil, fmt.Errorf("DRM service_index must be in 0..3")
		}
		commands = append(commands, fmt.Sprintf("action drm_service %d", *a.ServiceIndex))
	}
	if a.Stereo != nil {
		v := 0
		if *a.Stereo {
			v = 1
		}
		commands = append(commands, fmt.Sprintf("action drm_stereo %d", v))
	}
	if a.IQInvert != "" {
		id, err := parseDRMIQInvert(a.IQInvert)
		if err != nil {
			return nil, err
		}
		commands = append(commands, fmt.Sprintf("action drm_iq_invert %d", id))
	}
	return commands, nil
}

func buildDRMControlCommand(a DRMControlArgs) (string, error) {
	switch a.Operation {
	case "start":
		if a.OpenPage {
			return "action drm_start 1", nil
		}
		return "action drm_start 0", nil
	case "stop":
		return "action drm_stop", nil
	case "reset":
		return "action drm_reset", nil
	case "service":
		if a.ServiceIndex == nil || *a.ServiceIndex < 0 || *a.ServiceIndex > 3 {
			return "", fmt.Errorf("provide service_index in 0..3")
		}
		return fmt.Sprintf("action drm_service %d", *a.ServiceIndex), nil
	case "profile":
		id, err := parseDRMProfile(a.Profile)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("action drm_profile %d", id), nil
	case "stereo":
		if a.Stereo == nil {
			return "", fmt.Errorf("provide stereo boolean")
		}
		if *a.Stereo {
			return "action drm_stereo 1", nil
		}
		return "action drm_stereo 0", nil
	case "iq_invert":
		id, err := parseDRMIQInvert(a.IQInvert)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("action drm_iq_invert %d", id), nil
	default:
		return "", fmt.Errorf("unsupported DRM operation %q", a.Operation)
	}
}
