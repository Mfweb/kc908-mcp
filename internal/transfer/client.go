// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

// Package transfer implements the binary KIMG transfer protocol for SD/SSTV
// images, background upload jobs, BMP preview transcoding, and WebAssembly v1
// .kcapp validation and deployment.
package transfer

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/mfweb/ktos-mcp/internal/device"
)

// Binary SD image / KCApp transfer protocol constants (shared with image_protocol.h).
const (
	// BackgroundThreshold is the payload size above which image uploads
	// automatically run in the background.
	BackgroundThreshold = 128 * 1024

	imageMagic      = 0x474d494b // "KIMG" little-endian
	imageHeader     = 320
	imageMax        = 16 * 1024 * 1024
	imageWriteChunk = 4000
	imageReadChunk  = 61440

	opImageUpload = 1
	opImageWrite  = 2
	opImageCommit = 3
	opImageOpen   = 4
	opImageRead   = 5
	opImageClose  = 6
	opImageRemove = 7
	opAppUpload   = 8
	opAppRemove   = 9

	formatPNG  = 1
	formatJPEG = 2
	formatBMP  = 3
)

var imageFormatMIME = map[uint32]string{
	formatPNG:  "image/png",
	formatJPEG: "image/jpeg",
	formatBMP:  "image/bmp",
}

var imageErrorReasons = map[int32]string{
	-7:   "image exceeds dimension or decode-memory limit",
	-12:  "not enough device memory",
	-16:  "SD card or image session busy",
	-19:  "SD card unavailable",
	-28:  "SD card full",
	-61:  "no decoded SSTV image",
	-74:  "invalid image or SHA-256 mismatch",
	-116: "SD card changed or image session expired",
	-125: "transfer cancelled",
}

// ImageInfo describes an uploaded or downloaded SD image.
type ImageInfo struct {
	DevicePath string `json:"device_path"`
	LocalPath  string `json:"local_path,omitempty"`
	Bytes      uint32 `json:"bytes"`
	Width      uint32 `json:"width"`
	Height     uint32 `json:"height"`
	MIME       string `json:"mime_type"`
	SHA256     string `json:"sha256"`
	DataBase64 string `json:"data_base64,omitempty"`

	session uint32
	offset  uint32
	format  uint32
}

// Client serializes binary KIMG transfers and manages background upload jobs.
type Client struct {
	device device.ImageDevice
	mu     sync.Mutex
	jobsMu sync.Mutex
	job    *imageJob
}

// NewClient creates a transfer client backed by dev (which may be nil when the
// underlying transport does not implement device.ImageDevice).
func NewClient(dev device.ImageDevice) *Client {
	return &Client{device: dev}
}

func encodeRequest(op, id, offset, count uint32, data []byte) []byte {
	packet := make([]byte, 16+len(data))
	binary.LittleEndian.PutUint32(packet[0:], op)
	binary.LittleEndian.PutUint32(packet[4:], id)
	binary.LittleEndian.PutUint32(packet[8:], offset)
	binary.LittleEndian.PutUint32(packet[12:], count)
	copy(packet[16:], data)
	return packet
}

func (c *Client) call(ctx context.Context, op, id, offset, count uint32, data []byte) (ImageInfo, []byte, error) {
	var info ImageInfo
	if c.device == nil {
		return info, nil, fmt.Errorf("image transfer unavailable; update SDK and firmware")
	}

	raw, err := c.device.ImageCall(ctx, encodeRequest(op, id, offset, count, data))
	if err != nil {
		return info, nil, err
	}
	if len(raw) < imageHeader || binary.LittleEndian.Uint32(raw) != imageMagic {
		return info, nil, fmt.Errorf("invalid image response; requires System 1.0.9 or later")
	}

	if code := int32(binary.LittleEndian.Uint32(raw[4:])); code != 0 {
		reason := imageErrorReasons[code]
		if reason == "" {
			reason = "image operation failed"
		}
		return info, nil, fmt.Errorf("%s (%d)", reason, code)
	}

	info.session = binary.LittleEndian.Uint32(raw[8:])
	info.Bytes = binary.LittleEndian.Uint32(raw[12:])
	info.offset = binary.LittleEndian.Uint32(raw[16:])
	info.Width = binary.LittleEndian.Uint32(raw[20:])
	info.Height = binary.LittleEndian.Uint32(raw[24:])
	info.format = binary.LittleEndian.Uint32(raw[28:])

	if info.Bytes > imageMax {
		return info, nil, fmt.Errorf("image size exceeds 16 MiB")
	}

	info.SHA256 = hex.EncodeToString(raw[32:64])
	end := bytes.IndexByte(raw[64:320], 0)
	if end < 0 {
		return info, nil, fmt.Errorf("unterminated device path")
	}
	info.DevicePath = string(raw[64 : 64+end])
	info.MIME = imageFormatMIME[info.format]

	return info, raw[320:], nil
}

func (c *Client) closeSession(id uint32) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, _ = c.call(ctx, opImageClose, id, 0, 0, nil)
}
