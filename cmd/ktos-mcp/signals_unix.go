// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

//go:build unix

package main

import (
	"os"
	"syscall"
)

func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
