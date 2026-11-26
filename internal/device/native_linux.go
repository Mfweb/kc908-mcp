// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

//go:build linux && cgo

package device

/*
#cgo linux LDFLAGS: -ldl

#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>

typedef int (*ktos_open_fn)(uint32_t, void **);
typedef void (*ktos_close_fn)(void *);
typedef int (*ktos_error_fn)(void *, char *, uint32_t);
typedef int (*ktos_rpc_fn)(void *, const char *, char *, uint32_t, uint32_t *);
typedef int (*ktos_image_request_fn)(void *, const void *, uint32_t, void *, uint32_t, uint32_t *);

typedef struct {
	void                 *lib;
	ktos_open_fn          open_fn;
	ktos_close_fn         close_fn;
	ktos_error_fn         error_fn;
	ktos_rpc_fn           rpc_fn;
	ktos_image_request_fn image_fn;
} ktos_linux_api;

static const char *ktos_linux_load(const char *path, ktos_linux_api *api) {
	dlerror();
	api->lib = dlopen(path, RTLD_NOW | RTLD_LOCAL);
	if (!api->lib) {
		const char *err = dlerror();
		return err ? err : "dlopen failed";
	}
	api->open_fn  = (ktos_open_fn)dlsym(api->lib, "ktos_open");
	api->close_fn = (ktos_close_fn)dlsym(api->lib, "ktos_close");
	api->error_fn = (ktos_error_fn)dlsym(api->lib, "ktos_error");
	api->rpc_fn   = (ktos_rpc_fn)dlsym(api->lib, "ktos_rpc");
	api->image_fn = (ktos_image_request_fn)dlsym(api->lib, "ktos_image_request");
	if (!api->open_fn || !api->close_fn || !api->error_fn || !api->rpc_fn) {
		dlclose(api->lib);
		api->lib = NULL;
		return "missing required KTOS SDK symbols";
	}
	return NULL;
}

static int ktos_linux_open(const ktos_linux_api *api, uint32_t index, uintptr_t *out,
                           char *err_buf, uint32_t err_cap) {
	void *dev  = NULL;
	int   code = api->open_fn(index, &dev);
	if (code != 0) {
		if (err_buf && err_cap > 0) {
			err_buf[0] = '\0';
			api->error_fn(NULL, err_buf, err_cap);
		}
		*out = 0;
		return code;
	}
	*out = (uintptr_t)dev;
	return 0;
}

static void ktos_linux_close(const ktos_linux_api *api, uintptr_t handle) {
	if (api && api->close_fn && handle) {
		api->close_fn((void *)handle);
	}
}

static int ktos_linux_error(const ktos_linux_api *api, uintptr_t handle, char *buf, uint32_t cap) {
	if (buf && cap > 0) {
		buf[0] = '\0';
	}
	return api->error_fn((void *)handle, buf, cap);
}

static int ktos_linux_rpc(const ktos_linux_api *api, uintptr_t handle, const char *cmd,
                          char *buf, uint32_t cap, uint32_t *needed) {
	return api->rpc_fn((void *)handle, cmd, buf, cap, needed);
}

static int ktos_linux_has_image(const ktos_linux_api *api) {
	return api && api->image_fn != NULL;
}

static int ktos_linux_image(const ktos_linux_api *api, uintptr_t handle, const void *req,
                            uint32_t req_len, void *resp, uint32_t resp_cap, uint32_t *actual) {
	return api->image_fn((void *)handle, req, req_len, resp, resp_cap, actual);
}
*/
import "C"

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"unsafe"
)

const sdkResponseCapacity = 65537

var (
	linuxLibMu sync.Mutex
	linuxLibs  = map[string]*C.ktos_linux_api{}
)

type nativeDevice struct {
	api    *C.ktos_linux_api
	handle C.uintptr_t
}

func loadLinuxSDK(full string) (*C.ktos_linux_api, error) {
	linuxLibMu.Lock()
	defer linuxLibMu.Unlock()

	if api, ok := linuxLibs[full]; ok {
		return api, nil
	}

	api := (*C.ktos_linux_api)(C.calloc(1, C.sizeof_ktos_linux_api))
	if api == nil {
		return nil, fmt.Errorf("load KTOS SDK: out of memory")
	}

	cPath := C.CString(full)
	defer C.free(unsafe.Pointer(cPath))

	if errMsg := C.ktos_linux_load(cPath, api); errMsg != nil {
		msg := C.GoString(errMsg)
		C.free(unsafe.Pointer(api))
		return nil, fmt.Errorf("load KTOS SDK (%s): %s", full, msg)
	}

	linuxLibs[full] = api
	return api, nil
}

func openNative(path string, index uint32) (*nativeDevice, error) {
	full, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	api, err := loadLinuxSDK(full)
	if err != nil {
		return nil, err
	}

	errBuf := make([]byte, 512)
	var handle C.uintptr_t
	code := C.ktos_linux_open(
		api,
		C.uint32_t(index),
		&handle,
		(*C.char)(unsafe.Pointer(&errBuf[0])),
		C.uint32_t(len(errBuf)),
	)
	if int32(code) != 0 {
		msg := cStringFromBytes(errBuf)
		return nil, fmt.Errorf(
			"open device: error %d: %s; enable KTOS remote MCP mode, connect USB3, and check udev permissions for 0403:601f",
			int32(code), msg,
		)
	}

	return &nativeDevice{api: api, handle: handle}, nil
}

func cStringFromBytes(buf []byte) string {
	if n := bytes.IndexByte(buf, 0); n >= 0 {
		buf = buf[:n]
	}
	return string(buf)
}

func readNativeError(api *C.ktos_linux_api, handle C.uintptr_t) string {
	buf := make([]byte, 512)
	C.ktos_linux_error(api, handle, (*C.char)(unsafe.Pointer(&buf[0])), C.uint32_t(len(buf)))
	return cStringFromBytes(buf)
}

func (d *nativeDevice) call(command string) (json.RawMessage, error) {
	cCmd := C.CString(command)
	defer C.free(unsafe.Pointer(cCmd))

	buffer := make([]byte, sdkResponseCapacity)
	var needed C.uint32_t

	code := C.ktos_linux_rpc(
		d.api,
		d.handle,
		cCmd,
		(*C.char)(unsafe.Pointer(&buffer[0])),
		C.uint32_t(len(buffer)),
		&needed,
	)
	if int32(code) != 0 {
		return nil, fmt.Errorf("SDK %d: %s", int32(code), readNativeError(d.api, d.handle))
	}
	if uint32(needed) < 2 || uint32(needed) > uint32(len(buffer)) {
		return nil, fmt.Errorf("invalid SDK response size %d", uint32(needed))
	}
	return json.RawMessage(buffer[:uint32(needed)-1]), nil
}

func (d *nativeDevice) close() {
	if d.handle != 0 {
		C.ktos_linux_close(d.api, d.handle)
		d.handle = 0
	}
	// Keep the loaded shared library cached until process exit.
}

func (d *nativeDevice) imageCall(request []byte) ([]byte, error) {
	if len(request) < 16 || len(request) > 4090 {
		return nil, fmt.Errorf("invalid binary image request")
	}
	if C.ktos_linux_has_image(d.api) == 0 {
		return nil, fmt.Errorf("image transfer requires SDK 1.0.2 or later")
	}

	response := make([]byte, sdkResponseCapacity)
	var actual C.uint32_t

	code := C.ktos_linux_image(
		d.api,
		d.handle,
		unsafe.Pointer(&request[0]),
		C.uint32_t(len(request)),
		unsafe.Pointer(&response[0]),
		C.uint32_t(len(response)),
		&actual,
	)
	if int32(code) != 0 {
		msg := readNativeError(d.api, d.handle)
		if msg != "" {
			return nil, fmt.Errorf("image transport SDK %d (%s); requires System 1.0.9 or later", int32(code), msg)
		}
		return nil, fmt.Errorf("image transport SDK %d; requires System 1.0.9 or later", int32(code))
	}
	if uint32(actual) > 65536 {
		return nil, fmt.Errorf("invalid image response length")
	}
	return response[:uint32(actual)], nil
}
