// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

package transfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"strings"
)

// ImageInput selects either a local file or a base64 payload for upload.
type ImageInput struct {
	LocalPath  string
	DataBase64 string
}

func detectImageFormat(data []byte) byte {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return formatPNG
	case bytes.HasPrefix(data, []byte{0xff, 0xd8}):
		return formatJPEG
	case bytes.HasPrefix(data, []byte("BM")):
		return formatBMP
	default:
		return 0
	}
}

// ReadImageBytes loads and size-checks raw PNG/JPEG/BMP bytes from either a
// local file or strict base64 text.
func ReadImageBytes(in ImageInput) ([]byte, error) {
	if (in.LocalPath == "") == (in.DataBase64 == "") {
		return nil, fmt.Errorf("provide exactly one of local_path or data_base64")
	}

	if in.DataBase64 != "" {
		if len(in.DataBase64) > base64.StdEncoding.EncodedLen(imageMax) {
			return nil, fmt.Errorf("image exceeds 16 MiB")
		}
		return base64.StdEncoding.Strict().DecodeString(in.DataBase64)
	}

	f, err := os.Open(in.LocalPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() || stat.Size() <= 0 || stat.Size() > imageMax {
		return nil, fmt.Errorf("expected regular image file up to 16 MiB")
	}
	return io.ReadAll(io.LimitReader(f, imageMax+1))
}

// UploadImage uploads a PNG/JPEG/BMP image synchronously into SD tmp and verifies SHA-256.
func (c *Client) UploadImage(ctx context.Context, data []byte) (ImageInfo, error) {
	if !c.mu.TryLock() {
		return ImageInfo{}, fmt.Errorf("image transfer busy; query image_transfer_status")
	}
	defer c.mu.Unlock()
	return c.uploadLocked(ctx, data, nil)
}

func (c *Client) uploadLocked(ctx context.Context, data []byte, progress func(uint32)) (ImageInfo, error) {
	format := detectImageFormat(data)
	if format == 0 || len(data) > imageMax {
		return ImageInfo{}, fmt.Errorf("expected PNG JPEG or BMP up to 16 MiB")
	}

	hash := sha256.Sum256(data)
	meta := append([]byte{format}, hash[:]...)

	info, _, err := c.call(ctx, opImageUpload, 0, 0, uint32(len(data)), meta)
	if err != nil {
		return info, err
	}

	id := info.session
	if id == 0 {
		return info, fmt.Errorf("invalid upload session")
	}
	defer c.closeSession(id)

	for offset := 0; offset < len(data); {
		end := offset + imageWriteChunk
		if end > len(data) {
			end = len(data)
		}

		part, _, writeErr := c.call(ctx, opImageWrite, id, uint32(offset), uint32(end-offset), data[offset:end])
		if writeErr != nil {
			return info, writeErr
		}
		if part.session != id || part.offset != uint32(end) {
			return info, fmt.Errorf("upload offset mismatch")
		}

		offset = end
		if progress != nil {
			progress(uint32(offset))
		}
	}

	info, _, err = c.call(ctx, opImageCommit, id, 0, 0, nil)
	if err == nil && (info.Bytes != uint32(len(data)) || info.SHA256 != hex.EncodeToString(hash[:])) {
		err = fmt.Errorf("upload SHA-256 mismatch")
	}
	return info, err
}

// DownloadImage downloads an SD image (or "@sstv" when path is empty) and verifies SHA-256.
func (c *Client) DownloadImage(ctx context.Context, path string) (ImageInfo, []byte, error) {
	if !c.mu.TryLock() {
		return ImageInfo{}, nil, fmt.Errorf("image transfer busy; query image_transfer_status")
	}
	defer c.mu.Unlock()

	if path == "" {
		path = "@sstv"
	}
	if len(path) > 255 || strings.IndexByte(path, 0) >= 0 {
		return ImageInfo{}, nil, fmt.Errorf("invalid device image path")
	}

	info, _, err := c.call(ctx, opImageOpen, 0, 0, 0, append([]byte(path), 0))
	if err != nil {
		return info, nil, err
	}
	if info.session == 0 || info.Bytes == 0 || info.MIME == "" {
		return info, nil, fmt.Errorf("invalid image metadata")
	}
	defer c.closeSession(info.session)

	result := make([]byte, 0, info.Bytes)
	for uint32(len(result)) < info.Bytes {
		part, chunk, readErr := c.call(ctx, opImageRead, info.session, uint32(len(result)), imageReadChunk, nil)
		if readErr != nil {
			return info, nil, readErr
		}
		if part.session != info.session || part.Bytes != info.Bytes || part.SHA256 != info.SHA256 ||
			len(chunk) == 0 || len(chunk) > int(info.Bytes)-len(result) ||
			part.offset != uint32(len(result)+len(chunk)) {
			return info, nil, fmt.Errorf("inconsistent image block")
		}
		result = append(result, chunk...)
	}

	hash := sha256.Sum256(result)
	if info.SHA256 != hex.EncodeToString(hash[:]) {
		return info, nil, fmt.Errorf("download SHA-256 mismatch")
	}
	return info, result, nil
}

// RemoveImage deletes one temporary KTOS image cache file on SD.
func (c *Client) RemoveImage(ctx context.Context, devicePath string) error {
	if len(devicePath) > 255 || devicePath == "" || strings.IndexByte(devicePath, 0) >= 0 {
		return fmt.Errorf("invalid cache path")
	}
	if !c.mu.TryLock() {
		return fmt.Errorf("image transfer busy; query image_transfer_status")
	}
	defer c.mu.Unlock()

	_, _, err := c.call(ctx, opImageRemove, 0, 0, 0, append([]byte(devicePath), 0))
	return err
}

// SaveDownload atomically writes downloaded bytes to a new local file without
// overwriting existing files.
func SaveDownload(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

// ImagePreview returns bytes suitable for MCP ImageContent. Uncompressed
// 24/32-bit BMP images are transcoded to PNG for client compatibility while
// keeping the downloaded file bytes untouched.
func ImagePreview(data []byte, mime string) ([]byte, string) {
	if mime != "image/bmp" {
		return data, mime
	}
	if len(data) < 54 || binary.LittleEndian.Uint32(data[14:]) < 40 {
		return nil, ""
	}

	w := int(int32(binary.LittleEndian.Uint32(data[18:])))
	h := int(int32(binary.LittleEndian.Uint32(data[22:])))
	topDown := h < 0
	if topDown {
		h = -h
	}

	bits := int(binary.LittleEndian.Uint16(data[28:]))
	offset := uint64(binary.LittleEndian.Uint32(data[10:]))
	if w <= 0 || h <= 0 || w > 2048 || h > 2048 || (bits != 24 && bits != 32) || binary.LittleEndian.Uint32(data[30:]) != 0 {
		return nil, ""
	}

	stride := (w*bits/8 + 3) &^ 3
	if offset+uint64(stride*h) > uint64(len(data)) {
		return nil, ""
	}

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row := y
		if !topDown {
			row = h - 1 - y
		}
		for x := 0; x < w; x++ {
			at := int(offset) + row*stride + x*bits/8
			img.SetNRGBA(x, y, color.NRGBA{
				R: data[at+2],
				G: data[at+1],
				B: data[at],
				A: 255,
			})
		}
	}

	var out bytes.Buffer
	if png.Encode(&out, img) != nil {
		return nil, ""
	}
	return out.Bytes(), "image/png"
}
