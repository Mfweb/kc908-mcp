// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

//go:build windows && amd64

package device

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

const sdkResponseCapacity = 65537

type nativeDevice struct {
	dll       *syscall.DLL
	handle    uintptr
	rpc       *syscall.Proc
	closeProc *syscall.Proc
	errorProc *syscall.Proc
}

func openNative(path string, index uint32) (*nativeDevice, error) {
	full, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	dll, err := syscall.LoadDLL(full)
	if err != nil {
		return nil, fmt.Errorf("load KTOS SDK: %w", err)
	}

	d := &nativeDevice{dll: dll}
	openProc, err := dll.FindProc("ktos_open")
	if err == nil {
		d.rpc, err = dll.FindProc("ktos_rpc")
	}
	if err == nil {
		d.closeProc, err = dll.FindProc("ktos_close")
	}
	if err == nil {
		d.errorProc, err = dll.FindProc("ktos_error")
	}
	if err != nil {
		dll.Release()
		return nil, err
	}

	// ktos_open() records failure text in OS thread-local FLS storage.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	code, _, _ := openProc.Call(uintptr(index), uintptr(unsafe.Pointer(&d.handle)))
	if int32(code) != 0 {
		msg := readNativeError(d.errorProc, 0)
		dll.Release()
		return nil, fmt.Errorf(
			"open device: error %d: %s; enable KTOS remote MCP mode and connect USB3",
			int32(code), msg,
		)
	}
	return d, nil
}

func readNativeError(errorProc *syscall.Proc, handle uintptr) string {
	buf := make([]byte, 512)
	errorProc.Call(handle, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n := bytes.IndexByte(buf, 0); n >= 0 {
		buf = buf[:n]
	}
	return string(buf)
}

func (d *nativeDevice) call(command string) (json.RawMessage, error) {
	cmdPtr, err := syscall.BytePtrFromString(command)
	if err != nil {
		return nil, err
	}

	buffer := make([]byte, sdkResponseCapacity)
	var needed uint32

	code, _, _ := d.rpc.Call(
		d.handle,
		uintptr(unsafe.Pointer(cmdPtr)),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
		uintptr(unsafe.Pointer(&needed)),
	)
	runtime.KeepAlive(cmdPtr)

	if int32(code) != 0 {
		return nil, fmt.Errorf("SDK %d: %s", int32(code), readNativeError(d.errorProc, d.handle))
	}
	if needed < 2 || needed > uint32(len(buffer)) {
		return nil, fmt.Errorf("invalid SDK response size %d", needed)
	}
	return json.RawMessage(buffer[:needed-1]), nil
}

func (d *nativeDevice) close() {
	if d.handle != 0 {
		d.closeProc.Call(d.handle)
		d.handle = 0
	}
	// Keep the native DLL resident until process exit to match the D3XX driver lifetime.
}

func (d *nativeDevice) imageCall(request []byte) ([]byte, error) {
	if len(request) < 16 || len(request) > 4090 {
		return nil, fmt.Errorf("invalid binary image request")
	}

	proc, err := d.dll.FindProc("ktos_image_request")
	if err != nil {
		return nil, fmt.Errorf("image transfer requires SDK 1.0.2 or later: %w", err)
	}

	response := make([]byte, sdkResponseCapacity)
	var actual uint32

	code, _, _ := proc.Call(
		d.handle,
		uintptr(unsafe.Pointer(&request[0])),
		uintptr(len(request)),
		uintptr(unsafe.Pointer(&response[0])),
		uintptr(len(response)),
		uintptr(unsafe.Pointer(&actual)),
	)
	runtime.KeepAlive(request)

	if int32(code) != 0 {
		return nil, fmt.Errorf("image transport SDK %d; requires System 1.0.9 or later", int32(code))
	}
	if actual > 65536 {
		return nil, fmt.Errorf("invalid image response length")
	}
	return response[:actual], nil
}
