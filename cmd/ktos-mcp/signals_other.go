// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

//go:build !unix

package main

import "os"

func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
