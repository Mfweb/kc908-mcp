// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

package device

import (
	"math"
	"strings"
	"testing"
)

func TestTelemetryEnvelope(t *testing.T) {
	valid := []string{
		`{"ok":true,"error":0,"phase":3}`,
		`{"ok":true,"error":-125,"phase":0}`,
		`{"ok":true,"error":"","output":0}`,
	}
	for _, response := range valid {
		got, err := DecodeResponse([]byte(response))
		if err != nil || string(got) != response {
			t.Fatalf("%s: %s, %v", response, got, err)
		}
	}

	invalid := []string{
		`{"ok":false,"code":-22,"error":"invalid parameter"}`,
		`{"ok":false,"error":-5}`,
		`not JSON`,
	}
	for _, response := range invalid {
		if _, err := DecodeResponse([]byte(response)); err == nil {
			t.Fatalf("accepted %s", response)
		}
	}

	_, err := DecodeResponse([]byte(`{"ok":false,"code":-22,"error":"参数无效"}`))
	if !strings.Contains(err.Error(), "参数无效") {
		t.Fatal(err)
	}
}

func TestParameterValidation(t *testing.T) {
	invalid := []Parameter{
		{Name: "x\naction", Value: 1},
		{Name: "x", Value: math.NaN()},
		{Name: "x", Value: math.Inf(1)},
		{Name: "", Value: 1},
	}
	for _, p := range invalid {
		if _, err := ParameterCommand(p); err == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
}
