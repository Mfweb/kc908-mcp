// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

//go:build (!windows || !amd64) && (!linux || !cgo)

package device

import (
	"encoding/json"
	"fmt"
)

type nativeDevice struct{}

func openNative(string, uint32) (*nativeDevice, error) {
	return nil, fmt.Errorf("USB transport requires Windows amd64 or Linux (built with cgo)")
}

func (*nativeDevice) call(string) (json.RawMessage, error) {
	return nil, fmt.Errorf("USB transport unavailable")
}

func (*nativeDevice) close() {}

func (*nativeDevice) imageCall([]byte) ([]byte, error) {
	return nil, fmt.Errorf("USB transport unavailable")
}
