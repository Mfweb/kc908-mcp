// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

package transfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const appMaxBytes = 4 * 1024 * 1024

// AppInput selects either a local .kcapp file or base64 payload for upload.
type AppInput struct {
	Filename   string
	LocalPath  string
	DataBase64 string
}

// AppUploadResult reports the verified device path and digest of an uploaded .kcapp.
type AppUploadResult struct {
	Filename   string `json:"filename"`
	DevicePath string `json:"device_path"`
	Bytes      uint32 `json:"bytes"`
	SHA256     string `json:"sha256"`
	Launched   bool   `json:"launched,omitempty"`
}

func isDOSReserved(stem string) bool {
	base := stem
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	upper := strings.ToUpper(base)
	switch upper {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) &&
		upper[3] >= '0' && upper[3] <= '9' {
		return true
	}
	return false
}

// ValidKCAppName enforces a flat, safe ASCII filename ending in ".kcapp".
func ValidKCAppName(name string) bool {
	if len(name) <= 6 || len(name) > 64 || !strings.HasSuffix(name, ".kcapp") {
		return false
	}
	stem := name[:len(name)-6]
	if isDOSReserved(stem) || stem[len(stem)-1] == '.' {
		return false
	}

	for i := 0; i < len(stem); i++ {
		c := stem[i]
		isAlnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if i == 0 && !isAlnum {
			return false
		}
		if !(isAlnum || c == '.' || c == '_' || c == '-') {
			return false
		}
		if c == '.' && i+1 < len(stem) && stem[i+1] == '.' {
			return false
		}
	}
	return true
}

// ValidWasmV1 performs structural validation of a WebAssembly 1.0 binary module.
func ValidWasmV1(data []byte) error {
	if len(data) < 8 || len(data) > appMaxBytes {
		return fmt.Errorf("expected WebAssembly v1 module between 8 bytes and 4 MiB")
	}
	if !bytes.Equal(data[:8], []byte{0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00}) {
		return fmt.Errorf("expected WebAssembly v1 magic header")
	}

	pos := 8
	for pos < len(data) {
		if data[pos] > 12 {
			return fmt.Errorf("invalid WebAssembly section id %d", data[pos])
		}
		pos++

		var secLen uint32
		var shift uint
		ended := false
		for used := 0; pos < len(data) && used < 5; used++ {
			b := data[pos]
			pos++
			if used == 4 && (b&0xf0) != 0 {
				return fmt.Errorf("invalid WebAssembly section length")
			}
			secLen |= uint32(b&0x7f) << shift
			if (b & 0x80) == 0 {
				ended = true
				break
			}
			shift += 7
		}
		if !ended || uint64(secLen) > uint64(len(data)-pos) {
			return fmt.Errorf("truncated WebAssembly section")
		}
		pos += int(secLen)
	}
	return nil
}

// ReadAppBytes loads and validates a .kcapp module from either a local file or
// strict base64 text, returning the validated bytes and resolved leaf filename.
func ReadAppBytes(in AppInput) ([]byte, string, error) {
	if (in.LocalPath == "") == (in.DataBase64 == "") {
		return nil, "", fmt.Errorf("provide exactly one of local_path or data_base64")
	}

	filename := in.Filename
	var data []byte
	var err error

	if in.DataBase64 != "" {
		if len(in.DataBase64) > base64.StdEncoding.EncodedLen(appMaxBytes) {
			return nil, "", fmt.Errorf("application exceeds 4 MiB")
		}
		data, err = base64.StdEncoding.Strict().DecodeString(in.DataBase64)
		if err != nil {
			return nil, "", err
		}
	} else {
		if filename == "" {
			filename = filepath.Base(in.LocalPath)
		}
		f, openErr := os.Open(in.LocalPath)
		if openErr != nil {
			return nil, "", openErr
		}
		defer f.Close()

		st, statErr := f.Stat()
		if statErr != nil {
			return nil, "", statErr
		}
		if !st.Mode().IsRegular() || st.Size() < 8 || st.Size() > appMaxBytes {
			return nil, "", fmt.Errorf("expected regular .kcapp file between 8 bytes and 4 MiB")
		}
		data, err = io.ReadAll(io.LimitReader(f, appMaxBytes+1))
		if err != nil {
			return nil, "", err
		}
	}

	if !ValidKCAppName(filename) {
		return nil, "", fmt.Errorf("invalid .kcapp filename %q: use safe ASCII leaf ending in .kcapp", filename)
	}
	if err := ValidWasmV1(data); err != nil {
		return nil, "", err
	}
	return data, filename, nil
}

// UploadApp transfers a validated .kcapp module to SD /apps/ and verifies SHA-256.
func (c *Client) UploadApp(ctx context.Context, filename string, data []byte) (AppUploadResult, error) {
	var out AppUploadResult
	if !ValidKCAppName(filename) {
		return out, fmt.Errorf("invalid .kcapp filename")
	}
	if err := ValidWasmV1(data); err != nil {
		return out, err
	}
	if !c.mu.TryLock() {
		return out, fmt.Errorf("transfer busy; retry after active image/app transfer completes")
	}
	defer c.mu.Unlock()

	hash := sha256.Sum256(data)
	meta := make([]byte, 32+len(filename)+1)
	copy(meta[:32], hash[:])
	copy(meta[32:], filename)

	info, _, err := c.call(ctx, opAppUpload, 0, 0, uint32(len(data)), meta)
	if err != nil {
		return out, err
	}

	id := info.session
	if id == 0 {
		return out, fmt.Errorf("invalid app upload session")
	}
	defer c.closeSession(id)

	for offset := 0; offset < len(data); {
		end := offset + imageWriteChunk
		if end > len(data) {
			end = len(data)
		}
		part, _, writeErr := c.call(ctx, opImageWrite, id, uint32(offset), uint32(end-offset), data[offset:end])
		if writeErr != nil {
			return out, writeErr
		}
		if part.session != id || part.offset != uint32(end) {
			return out, fmt.Errorf("app upload offset mismatch")
		}
		offset = end
	}

	info, _, err = c.call(ctx, opImageCommit, id, 0, 0, nil)
	if err != nil {
		return out, err
	}

	expectedHex := hex.EncodeToString(hash[:])
	if info.Bytes != uint32(len(data)) || info.SHA256 != expectedHex {
		return out, fmt.Errorf("app upload SHA-256 mismatch")
	}

	out = AppUploadResult{
		Filename:   filename,
		DevicePath: info.DevicePath,
		Bytes:      info.Bytes,
		SHA256:     info.SHA256,
	}
	return out, nil
}

// RemoveApp deletes one exact regular .kcapp file under SD /apps/.
func (c *Client) RemoveApp(ctx context.Context, filename string) error {
	if !ValidKCAppName(filename) {
		return fmt.Errorf("invalid .kcapp filename")
	}
	if !c.mu.TryLock() {
		return fmt.Errorf("transfer busy; retry after active transfer completes")
	}
	defer c.mu.Unlock()

	_, _, err := c.call(ctx, opAppRemove, 0, 0, 0, append([]byte(filename), 0))
	return err
}
