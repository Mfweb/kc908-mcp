// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

package transfer

import "testing"

func TestAppValidation(t *testing.T) {
	validWasm := []byte{0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00, 0x00, 0x04, 0x01, 'x', 'y', 'z'}
	if err := ValidWasmV1(validWasm); err != nil {
		t.Fatalf("valid wasm rejected: %v", err)
	}
	if err := ValidWasmV1([]byte("\x7fELF\x01\x01\x01\x00")); err == nil {
		t.Fatal("ELF binary accepted")
	}
	if err := ValidWasmV1([]byte{0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00, 0x01, 0x40, 0x00}); err == nil {
		t.Fatal("truncated wasm accepted")
	}

	for _, bad := range []string{"", "../a.kcapp", "a/b.kcapp", ".hidden.kcapp", "CON.kcapp", "x..y.kcapp", "bad.wasm"} {
		if ValidKCAppName(bad) {
			t.Fatalf("invalid filename accepted: %q", bad)
		}
	}
	if !ValidKCAppName("sat_tracker-1.0.kcapp") {
		t.Fatal("valid filename rejected")
	}
}
