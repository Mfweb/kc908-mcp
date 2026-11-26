// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

//go:build linux && cgo

package device

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxMissingSDK(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nonexistent-libkcsdr.so")
	dev := NewLocked(missing, 0)
	defer dev.Close()

	_, err := dev.Call(context.Background(), "capabilities")
	if err == nil || !strings.Contains(err.Error(), "load KTOS SDK") {
		t.Fatalf("expected SDK load error, got: %v", err)
	}
}

func TestLinuxNativeSDK(t *testing.T) {
	sdkPath := os.Getenv("KTOS_TEST_SDK")
	if sdkPath == "" {
		t.Skip("set KTOS_TEST_SDK to built tests/libkcsdr.so to run Linux native SDK integration tests")
	}

	t.Setenv("KTOS_TEST_MODE", "2")
	t.Setenv("KTOS_TEST_FAILURE", "")
	t.Setenv("KTOS_TEST_IMAGE", "")

	dev := NewLocked(sdkPath, 0)
	defer dev.Close()

	ctx := context.Background()

	// Basic capability call and large fragmented JSON RPC (16 KiB).
	raw, err := dev.Call(ctx, "capabilities")
	if err != nil || !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("capabilities failed: raw=%s err=%v", raw, err)
	}

	fft, err := dev.Call(ctx, "fft")
	if err != nil || len(fft) != 16000 {
		t.Fatalf("fragmented fft failed: len=%d err=%v", len(fft), err)
	}

	// VSA, GPS, and DRM telemetry paths plus device ok=false error mapping.
	vsa, err := dev.Call(ctx, "vsa")
	if err != nil || !strings.Contains(string(vsa), `"16QAM"`) {
		t.Fatalf("vsa failed: raw=%s err=%v", vsa, err)
	}
	drm, err := dev.Call(ctx, "drm")
	if err != nil || !strings.Contains(string(drm), `"DRM Live"`) {
		t.Fatalf("drm failed: raw=%s err=%v", drm, err)
	}
	if _, err := dev.Call(ctx, "action gps_sync_location"); err == nil || !strings.Contains(err.Error(), "device error -11") {
		t.Fatalf("expected device error -11, got: %v", err)
	}

	// Binary KIMG request path (echo mode).
	payload := make([]byte, 256)
	for i := range payload {
		payload[i] = byte(i)
	}
	resp, err := dev.ImageCall(ctx, payload)
	if err != nil || !bytes.Equal(resp, payload) {
		t.Fatalf("ImageCall echo mismatch: len=%d err=%v", len(resp), err)
	}

	// Transport failure must release handle and transparently reopen on next call.
	t.Setenv("KTOS_TEST_FAILURE", "5")
	if _, err := dev.Call(ctx, "capabilities"); err == nil {
		t.Fatal("expected injected control write failure")
	}
	t.Setenv("KTOS_TEST_FAILURE", "")
	if raw, err := dev.Call(ctx, "capabilities"); err != nil || !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("recovery after failure failed: raw=%s err=%v", raw, err)
	}
}
