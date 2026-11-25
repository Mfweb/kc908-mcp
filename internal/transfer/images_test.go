// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

package transfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeImages struct {
	data     []byte
	expected [32]byte
	id       uint32
	offset   uint32
	closed   int
	corrupt  bool
	writes   int
}

func (d *fakeImages) ImageCall(ctx context.Context, p []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	op := binary.LittleEndian.Uint32(p)
	id := binary.LittleEndian.Uint32(p[4:])
	offset := binary.LittleEndian.Uint32(p[8:])
	count := binary.LittleEndian.Uint32(p[12:])

	out := make([]byte, imageHeader)
	put := func(at int, v uint32) { binary.LittleEndian.PutUint32(out[at:], v) }
	put(0, imageMagic)

	var payload []byte
	switch op {
	case opImageUpload:
		if d.id != 0 {
			return nil, fmt.Errorf("busy")
		}
		d.id = 42
		d.offset = 0
		d.data = make([]byte, count)
		copy(d.expected[:], p[17:49])
	case opImageWrite:
		if id != d.id || offset != d.offset || int(count) != len(p)-16 {
			return nil, fmt.Errorf("bad write")
		}
		copy(d.data[offset:], p[16:])
		d.offset += count
		d.writes++
	case opImageCommit:
		if sha256.Sum256(d.data) != d.expected {
			return nil, fmt.Errorf("checksum")
		}
	case opImageOpen:
		if d.id != 0 {
			return nil, fmt.Errorf("busy")
		}
		d.id = 44
		d.offset = 0
	case opImageRead:
		if id != d.id || offset > uint32(len(d.data)) {
			return nil, fmt.Errorf("bad read")
		}
		end := offset + count
		if end > uint32(len(d.data)) {
			end = uint32(len(d.data))
		}
		payload = append([]byte(nil), d.data[offset:end]...)
		d.offset = end
		if d.corrupt && len(payload) > 0 {
			payload[0] ^= 1
		}
	case opImageClose:
		if id != d.id {
			return nil, fmt.Errorf("stale")
		}
		d.id = 0
		d.closed++
	case opImageRemove:
		d.data = nil
	}

	put(8, d.id)
	put(12, uint32(len(d.data)))
	put(16, d.offset)
	put(20, 2)
	put(24, 2)
	put(28, formatBMP)
	hash := sha256.Sum256(d.data)
	copy(out[32:64], hash[:])
	copy(out[64:], "/mnt/sdcard/tmp/ktos-image-0123456789abcdef0123456789abcdef.bmp")
	return append(out, payload...), nil
}

type blockedImages struct {
	fakeImages
	entered chan struct{}
	once    sync.Once
}

func (d *blockedImages) ImageCall(ctx context.Context, p []byte) ([]byte, error) {
	if binary.LittleEndian.Uint32(p) == opImageWrite {
		d.once.Do(func() { close(d.entered) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return d.fakeImages.ImageCall(ctx, p)
}

func testBMP() []byte {
	b := make([]byte, 54+128*192*4)
	copy(b, "BM")
	binary.LittleEndian.PutUint32(b[2:], uint32(len(b)))
	binary.LittleEndian.PutUint32(b[10:], 54)
	binary.LittleEndian.PutUint32(b[14:], 40)
	binary.LittleEndian.PutUint32(b[18:], 128)
	binary.LittleEndian.PutUint32(b[22:], 192)
	b[26] = 1
	b[28] = 32
	for i := 54; i < len(b); i++ {
		b[i] = byte(i)
	}
	return b
}

func waitJob(t *testing.T, c *Client, id string) JobStatus {
	t.Helper()
	limit := time.After(2 * time.Second)
	for {
		s, err := c.TransferStatus(id, false)
		if err != nil {
			t.Fatal(err)
		}
		if s.State != "uploading" && s.State != "cancelling" {
			return s
		}
		select {
		case <-limit:
			t.Fatal("job did not terminate")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestImageTransfer(t *testing.T) {
	ctx := context.Background()
	d := new(fakeImages)
	c := NewClient(d)
	original := testBMP()

	info, err := c.UploadImage(ctx, original)
	if err != nil || info.Bytes != uint32(len(original)) || d.closed != 1 || d.writes < 2 {
		t.Fatalf("upload: %+v %v", info, err)
	}

	got, data, err := c.DownloadImage(ctx, info.DevicePath)
	if err != nil || !bytes.Equal(data, original) || got.SHA256 != info.SHA256 || d.closed != 2 {
		t.Fatalf("download: %+v %v", got, err)
	}

	preview, mime := ImagePreview(data, "image/bmp")
	if mime != "image/png" || !bytes.HasPrefix(preview, []byte("\x89PNG")) {
		t.Fatal("BMP preview")
	}

	d.corrupt = true
	if _, _, err = c.DownloadImage(ctx, "@sstv"); err == nil {
		t.Fatal("accepted corrupted download")
	}
	if d.id != 0 {
		t.Fatal("session leaked")
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = c.UploadImage(cancelled, original); err == nil {
		t.Fatal("ignored cancellation")
	}
	if _, err = c.UploadImage(ctx, []byte("invalid")); err == nil {
		t.Fatal("accepted non-image")
	}

	tmp := filepath.Join(t.TempDir(), "图像.bmp")
	if err = SaveDownload(tmp, original); err != nil {
		t.Fatal(err)
	}
	if err = SaveDownload(tmp, []byte("bad")); err == nil {
		t.Fatal("overwrote existing file")
	}

	for _, args := range []ImageInput{
		{LocalPath: tmp},
		{DataBase64: base64.StdEncoding.EncodeToString(original)},
	} {
		b, e := ReadImageBytes(args)
		if e != nil || !bytes.Equal(b, original) {
			t.Fatal("input", e)
		}
	}

	for _, args := range []ImageInput{
		{},
		{LocalPath: tmp, DataBase64: "AA=="},
		{DataBase64: "!"},
	} {
		if _, err = ReadImageBytes(args); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
}

func TestImageJobCompletion(t *testing.T) {
	d := new(fakeImages)
	c := NewClient(d)
	defer c.Shutdown()

	s, err := c.StartUpload(testBMP())
	if err != nil || s.State != "uploading" || s.Result != nil {
		t.Fatal(s, err)
	}

	done := waitJob(t, c, s.TransferID)
	if done.State != "completed" || done.Progress != 100 || done.Result == nil ||
		done.Transferred != done.Total || d.id != 0 || d.closed != 1 {
		t.Fatal(done, d.id, d.closed)
	}

	if _, err = c.TransferStatus("wrong", true); err == nil {
		t.Fatal("accepted stale ID")
	}

	next, err := c.StartUpload(testBMP())
	if err != nil || next.TransferID == s.TransferID {
		t.Fatal(next, err)
	}
	if _, err = c.TransferStatus(s.TransferID, false); err == nil {
		t.Fatal("retained stale job")
	}
	waitJob(t, c, next.TransferID)
}

func TestImageJobCancellationAndShutdown(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		d := &blockedImages{entered: make(chan struct{})}
		c := NewClient(d)

		s, err := c.StartUpload(testBMP())
		if err != nil {
			t.Fatal(err)
		}

		select {
		case <-d.entered:
		case <-time.After(time.Second):
			t.Fatal("not started")
		}

		if _, err = c.StartUpload(testBMP()); err == nil {
			t.Fatal("parallel upload")
		}
		if _, _, err = c.DownloadImage(context.Background(), "@sstv"); err == nil {
			t.Fatal("blocked download")
		}
		if _, err = c.UploadImage(context.Background(), testBMP()); err == nil {
			t.Fatal("blocked upload")
		}

		if shutdown {
			c.Shutdown()
		} else {
			state, err := c.TransferStatus(s.TransferID, true)
			if err != nil || state.State != "cancelling" {
				t.Fatal(state, err)
			}
		}

		done := waitJob(t, c, s.TransferID)
		if done.State != "cancelled" || done.Result != nil || d.id != 0 || d.closed != 1 {
			t.Fatal(done, d.id, d.closed)
		}
		c.Shutdown()
	}
}
