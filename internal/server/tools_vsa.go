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

var vsaModulations = map[string]int{
	"bpsk":       0,
	"qpsk":       1,
	"oqpsk":      2,
	"pi4-dqpsk":  3,
	"pi/4-dqpsk": 3,
	"pi4dqpsk":   3,
	"8psk":       4,
	"16qam":      5,
	"64qam":      6,
	"2fsk":       7,
	"4fsk":       8,
	"msk":        9,
	"gmsk":       10,
}

var vsaFilters = map[string]int{
	"rrc":      0,
	"rc":       1,
	"gaussian": 2,
	"rect":     3,
	"none":     3,
}

var vsaEqualizers = map[string]int{
	"off":      0,
	"adaptive": 1,
	"on":       1,
}

var vsaViews = map[string]int{
	"spectrum": 0,
	"eye":      1,
	"evm":      2,
}

var vsaFrameSections = map[string]struct{}{
	"all":           {},
	"summary":       {},
	"constellation": {},
	"spectrum":      {},
	"evm":           {},
	"eye":           {},
}

type VSAConfigureArgs struct {
	Enter          bool     `json:"enter,omitempty" jsonschema:"Switch to VSA mode (mode 6) before applying settings"`
	Preset         *int     `json:"preset,omitempty" jsonschema:"Optional VSA preset index 0..6 (0=QPSK, 1=16QAM, 2=64QAM, 3=8PSK, 4=TETRA 18k, 5=DMR/P25 4FSK, 6=GMSK 9.6k)"`
	FrequencyHz    *float64 `json:"frequency_hz,omitempty" jsonschema:"Center frequency in Hz"`
	Modulation     string   `json:"modulation,omitempty" jsonschema:"bpsk, qpsk, oqpsk, pi4-dqpsk, 8psk, 16qam, 64qam, 2fsk, 4fsk, msk, or gmsk"`
	SymbolRateHz   *float64 `json:"symbol_rate_hz,omitempty" jsonschema:"Symbol rate in sym/s (1000..2000000)"`
	Filter         string   `json:"filter,omitempty" jsonschema:"Pulse shaping filter: rrc, rc, gaussian, or rect"`
	Alpha          *float64 `json:"alpha,omitempty" jsonschema:"Filter rolloff / BT factor (0.10..1.00)"`
	Equalizer      string   `json:"equalizer,omitempty" jsonschema:"off or adaptive"`
	Symbols        *int     `json:"symbols,omitempty" jsonschema:"Analysis symbol count: 128, 256, 512, or 1024"`
	Average        *int     `json:"average,omitempty" jsonschema:"Averaging frames (1..64)"`
	FSKDeviationHz *float64 `json:"fsk_deviation_hz,omitempty" jsonschema:"FSK deviation in Hz (0 for auto, up to 1000000)"`
	Port           *int     `json:"port,omitempty" jsonschema:"RF input panel port (1 or 2)"`
	AttDB          *int     `json:"att_db,omitempty" jsonschema:"RF attenuation in dB (0..30)"`
	AmpMode        *int     `json:"amp_mode,omitempty" jsonschema:"Preamp mode: 0 (0 dB), 1 (20 dB), or 2 (40 dB)"`
	IFGainDB       *int     `json:"if_gain_db,omitempty" jsonschema:"IF gain in dB (0..30)"`
}

type VSAControlArgs struct {
	Operation string `json:"operation" jsonschema:"enter, run, stop, single, clear_persist, preset, or view"`
	Preset    *int   `json:"preset,omitempty" jsonschema:"Preset index 0..6 when operation is preset"`
	View      string `json:"view,omitempty" jsonschema:"Secondary view (spectrum, eye, or evm) when operation is view"`
}

type VSAReadArgs struct {
	Section string `json:"section,omitempty" jsonschema:"Frame section: all (default), summary, constellation, spectrum, evm, or eye"`
}

func addVSATools(s *mcp.Server, dev device.Device) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "vsa_configure",
		Description: "Configure Vector Signal Analysis (VSA) parameters: center frequency, modulation, symbol rate, pulse filter, alpha, equalizer, symbol count, averaging, FSK deviation, RF port/gain, or built-in preset.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a VSAConfigureArgs) (*mcp.CallToolResult, any, error) {
		commands, err := buildVSAConfigureCommands(a)
		if err != nil {
			return toolResult(nil, err)
		}
		if len(commands) == 0 {
			return toolResult(nil, fmt.Errorf("provide at least one VSA configuration field"))
		}

		for i, cmd := range commands {
			if _, err := dev.Call(ctx, cmd); err != nil {
				return toolResult(nil, fmt.Errorf("%d applied; %s: %w", i, cmd, err))
			}
		}
		return toolResult(json.RawMessage(fmt.Sprintf(`{"ok":true,"applied":%d}`, len(commands))), nil)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "vsa_control",
		Description: "Control Vector Signal Analysis: enter VSA mode, start/stop continuous capture, trigger single-shot measurement, clear constellation persistence, apply preset (0..6), or switch secondary plot view (spectrum/eye/evm).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a VSAControlArgs) (*mcp.CallToolResult, any, error) {
		cmd, err := buildVSAControlCommand(a)
		if err != nil {
			return toolResult(nil, err)
		}
		return toolResult(dev.Call(ctx, cmd))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "vsa_status",
		Description: "Read live VSA configuration, lock/overload state, and vector demodulation metrics (EVM RMS/Peak/dB, MER, SNR, CFO, magnitude/phase error, IQ offset/quadrature/gain imbalance, FSK deviation, modulation index, power, symbol-rate error).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, any, error) {
		return toolResult(dev.Call(ctx, "vsa"))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "vsa_read",
		Description: "Read the latest VSA measurement frame snapshot (constellation I/Q points, reference symbols, baseband spectrum, EVM trace, and/or eye diagram traces).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a VSAReadArgs) (*mcp.CallToolResult, any, error) {
		sec := strings.ToLower(strings.TrimSpace(a.Section))
		if sec == "" {
			sec = "all"
		}
		if _, ok := vsaFrameSections[sec]; !ok {
			return toolResult(nil, fmt.Errorf("invalid VSA frame section %q", a.Section))
		}
		return toolResult(dev.Call(ctx, "vsa_frame "+sec))
	})
}

func buildVSAConfigureCommands(a VSAConfigureArgs) ([]string, error) {
	var commands []string
	addParam := func(name string, value float64) error {
		cmd, err := device.ParameterCommand(device.Parameter{Name: name, Value: value})
		if err != nil {
			return err
		}
		commands = append(commands, cmd)
		return nil
	}

	if a.Enter {
		commands = append(commands, "mode 6")
	}
	if a.Preset != nil {
		if *a.Preset < 0 || *a.Preset > 6 {
			return nil, fmt.Errorf("VSA preset must be in 0..6")
		}
		commands = append(commands, fmt.Sprintf("action vsa_preset %d", *a.Preset))
	}
	if a.Port != nil {
		if *a.Port != 1 && *a.Port != 2 {
			return nil, fmt.Errorf("VSA port must be 1 or 2")
		}
		if err := addParam("vsa.port", float64(*a.Port)); err != nil {
			return nil, err
		}
	}
	if a.FrequencyHz != nil {
		if err := addParam("vsa.frequency", *a.FrequencyHz); err != nil {
			return nil, err
		}
	}
	if a.Modulation != "" {
		mod, ok := vsaModulations[strings.ToLower(strings.TrimSpace(a.Modulation))]
		if !ok {
			return nil, fmt.Errorf("unsupported VSA modulation %q", a.Modulation)
		}
		if err := addParam("vsa.modulation", float64(mod)); err != nil {
			return nil, err
		}
	}
	if a.SymbolRateHz != nil {
		if err := addParam("vsa.symbol_rate", *a.SymbolRateHz); err != nil {
			return nil, err
		}
	}
	if a.Filter != "" {
		flt, ok := vsaFilters[strings.ToLower(strings.TrimSpace(a.Filter))]
		if !ok {
			return nil, fmt.Errorf("unsupported VSA filter %q", a.Filter)
		}
		if err := addParam("vsa.filter", float64(flt)); err != nil {
			return nil, err
		}
	}
	if a.Alpha != nil {
		if err := addParam("vsa.alpha", *a.Alpha); err != nil {
			return nil, err
		}
	}
	if a.Equalizer != "" {
		eq, ok := vsaEqualizers[strings.ToLower(strings.TrimSpace(a.Equalizer))]
		if !ok {
			return nil, fmt.Errorf("unsupported VSA equalizer %q", a.Equalizer)
		}
		if err := addParam("vsa.equalizer", float64(eq)); err != nil {
			return nil, err
		}
	}
	if a.Symbols != nil {
		if *a.Symbols != 128 && *a.Symbols != 256 && *a.Symbols != 512 && *a.Symbols != 1024 {
			return nil, fmt.Errorf("VSA symbols must be 128, 256, 512, or 1024")
		}
		if err := addParam("vsa.symbols", float64(*a.Symbols)); err != nil {
			return nil, err
		}
	}
	if a.Average != nil {
		if err := addParam("vsa.average", float64(*a.Average)); err != nil {
			return nil, err
		}
	}
	if a.FSKDeviationHz != nil {
		if err := addParam("vsa.fsk_deviation", *a.FSKDeviationHz); err != nil {
			return nil, err
		}
	}
	if a.AttDB != nil {
		if err := addParam("vsa.att", float64(*a.AttDB)); err != nil {
			return nil, err
		}
	}
	if a.AmpMode != nil {
		if *a.AmpMode < 0 || *a.AmpMode > 2 {
			return nil, fmt.Errorf("VSA amp_mode must be 0, 1, or 2")
		}
		if err := addParam("vsa.amp", float64(*a.AmpMode)); err != nil {
			return nil, err
		}
	}
	if a.IFGainDB != nil {
		if err := addParam("vsa.if_gain", float64(*a.IFGainDB)); err != nil {
			return nil, err
		}
	}
	return commands, nil
}

func buildVSAControlCommand(a VSAControlArgs) (string, error) {
	switch a.Operation {
	case "enter":
		return "mode 6", nil
	case "run":
		return "action run", nil
	case "stop":
		return "action stop", nil
	case "single":
		return "action vsa_single", nil
	case "clear_persist":
		return "action vsa_clear_persist", nil
	case "preset":
		if a.Preset == nil || *a.Preset < 0 || *a.Preset > 6 {
			return "", fmt.Errorf("provide preset in 0..6")
		}
		return fmt.Sprintf("action vsa_preset %d", *a.Preset), nil
	case "view":
		viewID, ok := vsaViews[strings.ToLower(strings.TrimSpace(a.View))]
		if !ok {
			return "", fmt.Errorf("provide view: spectrum, eye, or evm")
		}
		return fmt.Sprintf("action vsa_view %d", viewID), nil
	default:
		return "", fmt.Errorf("unsupported VSA operation %q", a.Operation)
	}
}
