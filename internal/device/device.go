// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

// Package device manages exclusive access to the KC908 KTOS USB 3.0 SDK,
// JSON RPC envelope decoding, and parameter command validation.
package device

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
)

// DefaultSDKName returns the platform-conventional shared library filename for
// the KTOS host SDK ("kcsdr.dll" on Windows, "libkcsdr.so" on Linux/Unix).
func DefaultSDKName() string {
	if runtime.GOOS == "windows" {
		return "kcsdr.dll"
	}
	return "libkcsdr.so"
}

// Device is the JSON control RPC transport exposed to MCP tools.
type Device interface {
	Call(ctx context.Context, command string) (json.RawMessage, error)
	Close()
}

// ImageDevice is the optional binary packet transport used for SD image and KCApp transfers.
type ImageDevice interface {
	ImageCall(ctx context.Context, request []byte) ([]byte, error)
}

// LockedDevice serializes USB access and lazily opens the native SDK handle on demand.
type LockedDevice struct {
	mu     sync.Mutex
	native *nativeDevice
	path   string
	index  uint32
}

// NewLocked creates a lazy, thread-safe device handle targeting the KTOS SDK
// shared library (kcsdr.dll / libkcsdr.so) at path.
func NewLocked(path string, index uint32) *LockedDevice {
	return &LockedDevice{path: path, index: index}
}

func (d *LockedDevice) ensureOpenLocked() error {
	if d.native != nil {
		return nil
	}
	native, err := openNative(d.path, d.index)
	if err != nil {
		return err
	}
	d.native = native
	return nil
}

// Call sends one control RPC command and validates the device JSON envelope.
func (d *LockedDevice) Call(ctx context.Context, command string) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(command) == 0 || len(command) > 4090 || strings.ContainsAny(command, "\r\n\x00") {
		return nil, fmt.Errorf("invalid device command")
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.ensureOpenLocked(); err != nil {
		return nil, err
	}

	response, err := d.native.call(command)
	if err != nil {
		// A driver handle cannot survive USB power cycling. Release it now so
		// the next request reopens the device. Never replay this call: a
		// mutating command may already have reached the radio before failure.
		d.native.close()
		d.native = nil
		return nil, err
	}
	return DecodeResponse(response)
}

// ImageCall sends one raw binary KIMG transfer packet over ktos_image_request.
func (d *LockedDevice) ImageCall(ctx context.Context, request []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.ensureOpenLocked(); err != nil {
		return nil, err
	}

	response, err := d.native.imageCall(request)
	if err != nil {
		d.native.close()
		d.native = nil
	}
	return response, err
}

// Close releases the underlying SDK device handle if currently open.
func (d *LockedDevice) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.native != nil {
		d.native.close()
		d.native = nil
	}
}

// DecodeResponse validates a JSON response from KTOS and converts ok=false
// envelopes into Go errors.
func DecodeResponse(response json.RawMessage) (json.RawMessage, error) {
	if !json.Valid(response) {
		return nil, fmt.Errorf("device returned invalid JSON")
	}

	var envelope struct {
		OK    *bool           `json:"ok"`
		Code  int             `json:"code"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return nil, err
	}

	if envelope.OK != nil && !*envelope.OK {
		message := string(envelope.Error)
		var text string
		if json.Unmarshal(envelope.Error, &text) == nil {
			message = text
		}
		return nil, fmt.Errorf("device error %d: %s", envelope.Code, message)
	}
	return response, nil
}

// Parameter is one named numeric instrument setting passed to configure tools.
type Parameter struct {
	Name  string  `json:"name" jsonschema:"Exact name from instrument_parameters"`
	Value float64 `json:"value" jsonschema:"Numeric value in the parameter's physical units"`
}

// ParameterCommand validates a Parameter and encodes it as a KTOS "set" RPC command.
func ParameterCommand(p Parameter) (string, error) {
	if p.Name == "" || len(p.Name) > 127 || strings.ContainsAny(p.Name, " \t\r\n\x00") ||
		math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
		return "", fmt.Errorf("invalid parameter")
	}
	return fmt.Sprintf("set %s %.17g", p.Name, p.Value), nil
}
