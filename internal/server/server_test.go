// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mfweb/ktos-mcp/internal/device"
)

type fakeDevice struct {
	mu       sync.Mutex
	commands []string
}

func (d *fakeDevice) Call(_ context.Context, c string) (json.RawMessage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.commands = append(d.commands, c)
	if strings.Contains(c, "bad") {
		return nil, fmt.Errorf("out of range")
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func (*fakeDevice) Close() {}

type fakeBinaryDevice struct {
	commands []string
	data     []byte
	apps     map[string][]byte
	upload   []byte
	target   string
	expected [32]byte
	id       uint32
	offset   uint32
	isApp    bool
}

func (d *fakeBinaryDevice) Call(_ context.Context, c string) (json.RawMessage, error) {
	d.commands = append(d.commands, c)
	if strings.HasPrefix(c, "app_console") {
		return json.RawMessage(`{"ok":true,"state":"running","active_file":"demo.kcapp","from":0,"next":6,"dropped":0,"text":"hello\n"}`), nil
	}
	if c == "apps" {
		return json.RawMessage(`{"ok":true,"state":"idle","count":1,"apps":[{"filename":"demo.kcapp"}]}`), nil
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func (*fakeBinaryDevice) Close() {}

func (d *fakeBinaryDevice) ImageCall(_ context.Context, p []byte) ([]byte, error) {
	op := binary.LittleEndian.Uint32(p)
	id := binary.LittleEndian.Uint32(p[4:])
	offset := binary.LittleEndian.Uint32(p[8:])
	count := binary.LittleEndian.Uint32(p[12:])

	out := make([]byte, 320)
	put := func(at int, v uint32) { binary.LittleEndian.PutUint32(out[at:], v) }
	put(0, 0x474d494b)

	if d.apps == nil {
		d.apps = map[string][]byte{}
	}

	var payload []byte
	switch op {
	case 1: // image upload
		d.isApp = false
		d.id = 42
		d.offset = 0
		d.data = make([]byte, count)
		copy(d.expected[:], p[17:49])
	case 8: // app upload
		d.isApp = true
		d.id = 77
		d.offset = 0
		copy(d.expected[:], p[16:48])
		nameBytes := p[48:]
		if len(nameBytes) == 0 || nameBytes[len(nameBytes)-1] != 0 {
			return nil, fmt.Errorf("bad name")
		}
		d.target = string(nameBytes[:len(nameBytes)-1])
		d.upload = make([]byte, count)
	case 2: // write
		if id != d.id || offset != d.offset {
			return nil, fmt.Errorf("bad write")
		}
		if d.isApp {
			copy(d.upload[offset:], p[16:])
		} else {
			copy(d.data[offset:], p[16:])
		}
		d.offset += count
	case 3: // commit
		if d.isApp {
			if sha256.Sum256(d.upload) != d.expected {
				return nil, fmt.Errorf("bad commit")
			}
			d.apps[d.target] = append([]byte(nil), d.upload...)
		} else if sha256.Sum256(d.data) != d.expected {
			return nil, fmt.Errorf("checksum")
		}
	case 4: // open
		d.isApp = false
		d.id = 44
		d.offset = 0
	case 5: // read
		if id != d.id || offset > uint32(len(d.data)) {
			return nil, fmt.Errorf("bad read")
		}
		end := offset + count
		if end > uint32(len(d.data)) {
			end = uint32(len(d.data))
		}
		payload = append([]byte(nil), d.data[offset:end]...)
		d.offset = end
	case 6: // close
		if id != d.id {
			return nil, fmt.Errorf("stale")
		}
		d.id = 0
	case 7: // remove image
		d.data = nil
	case 9: // remove app
		name := strings.TrimSuffix(string(p[16:]), "\x00")
		delete(d.apps, name)
	default:
		return nil, fmt.Errorf("unexpected op %d", op)
	}

	put(8, d.id)
	if d.isApp {
		put(12, uint32(len(d.upload)))
		put(16, d.offset)
		put(28, 4)
		hash := sha256.Sum256(d.upload)
		copy(out[32:64], hash[:])
		copy(out[64:], "/mnt/sdcard/apps/"+d.target)
	} else {
		put(12, uint32(len(d.data)))
		put(16, d.offset)
		put(20, 2)
		put(24, 2)
		put(28, 3)
		hash := sha256.Sum256(d.data)
		copy(out[32:64], hash[:])
		copy(out[64:], "/mnt/sdcard/tmp/ktos-image-0123456789abcdef0123456789abcdef.bmp")
	}
	return append(out, payload...), nil
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

func connectTestClient(t *testing.T, ctx context.Context, dev device.Device) (*mcp.ClientSession, func()) {
	t.Helper()
	srv := New(dev, "test")
	a, b := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	cs, err := client.Connect(ctx, b, nil)
	if err != nil {
		ss.Close()
		t.Fatal(err)
	}
	return cs, func() {
		cs.Close()
		ss.Close()
	}
}

func TestInstrumentMCPTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dev := new(fakeDevice)
	cs, cleanup := connectTestClient(t, ctx, dev)
	defer cleanup()

	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 33 {
		t.Fatalf("tools: %d", len(list.Tools))
	}

	cases := []struct {
		name    string
		args    any
		command string
		fail    bool
	}{
		{"instrument_mode", ModeArgs{Mode: "receiver"}, "mode 0", false},
		{"instrument_mode", ModeArgs{Mode: "correlator"}, "mode 3", false},
		{"instrument_mode", ModeArgs{Mode: "vsa"}, "mode 6", false},
		{"instrument_status", TelemetryArgs{Topic: "adsb"}, "adsb", false},
		{"instrument_status", TelemetryArgs{Topic: "dtmb"}, "dtmb", false},
		{"instrument_status", TelemetryArgs{Topic: "vsa"}, "vsa", false},
		{"instrument_status", TelemetryArgs{Topic: "gps"}, "gps", false},
		{"receiver_configure", ConfigureArgs{Parameters: []device.Parameter{{Name: "RX_FREQ", Value: 144640000}}}, "set RX_FREQ 144640000", false},
		{"instrument_text", TextArgs{Field: "channel_rename:4", Text: "济南广播"}, "text channel_rename:4 e6b58ee58d97e5b9bfe692ad", false},
		{"instrument_text", TextArgs{Field: "channel_add", Text: "FM"}, "text channel_add 464d", false},
		{"instrument_input", InputArgs{Kind: "key", Value: 0x8047}, "key 8047", false},
		{"spectrum_read", Empty{}, "fft", false},
		{"instrument_mode", ModeArgs{Mode: "transmitter"}, "", true},
		{"instrument_input", InputArgs{Kind: "click", Value: 800}, "", true},
		{"instrument_get", GetArgs{Name: "x\naction run"}, "", true},
		{"receiver_configure", ConfigureArgs{Parameters: []device.Parameter{{Name: "bad", Value: 1}}}, "set bad 1", true},
	}

	for _, test := range cases {
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: test.name, Arguments: test.args})
		if err != nil {
			t.Fatal(err)
		}
		if r.IsError != test.fail {
			t.Fatalf("%s error=%v content=%v", test.name, r.IsError, r.Content)
		}
		if test.command != "" {
			dev.mu.Lock()
			last := dev.commands[len(dev.commands)-1]
			dev.mu.Unlock()
			if last != test.command {
				t.Fatalf("%q != %q", last, test.command)
			}
		}
	}
}

func TestImageMCPTools(t *testing.T) {
	ctx := context.Background()
	dev := new(fakeBinaryDevice)
	cs, cleanup := connectTestClient(t, ctx, dev)
	defer cleanup()

	input := filepath.Join(t.TempDir(), "input.bmp")
	if err := os.WriteFile(input, testBMP(), 0600); err != nil {
		t.Fatal(err)
	}

	r, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "image_upload",
		Arguments: ImageUploadArgs{LocalPath: input},
	})
	if err != nil || r.IsError {
		t.Fatalf("upload tool %v %+v", err, r)
	}

	output := filepath.Join(t.TempDir(), "received.bmp")
	r, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "image_download",
		Arguments: ImageDownloadArgs{LocalPath: output, IncludeBase64: true},
	})
	if err != nil || r.IsError || len(r.Content) != 2 {
		t.Fatalf("download tool %v %+v", err, r)
	}

	disk, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(disk, testBMP()) {
		t.Fatal("disk mismatch", err)
	}
	if _, ok := r.Content[1].(*mcp.ImageContent); !ok {
		t.Fatal("missing image content")
	}

	r, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "image_remove",
		Arguments: ImageRemoveArgs{DevicePath: "/mnt/sdcard/tmp/ktos-image-0123456789abcdef0123456789abcdef.bmp"},
	})
	if err != nil || r.IsError || len(dev.data) != 0 {
		t.Fatal("remove", err)
	}
}

func TestAppMCPTools(t *testing.T) {
	ctx := context.Background()
	dev := new(fakeBinaryDevice)
	cs, cleanup := connectTestClient(t, ctx, dev)
	defer cleanup()

	validWasm := []byte{0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00, 0x00, 0x04, 0x01, 'x', 'y', 'z'}
	local := filepath.Join(t.TempDir(), "demo.kcapp")
	if err := os.WriteFile(local, validWasm, 0600); err != nil {
		t.Fatal(err)
	}

	r, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "app_upload",
		Arguments: AppUploadArgs{LocalPath: local, Launch: true},
	})
	if err != nil || r.IsError {
		t.Fatalf("app_upload failed: %v %+v", err, r)
	}
	if !bytes.Equal(dev.apps["demo.kcapp"], validWasm) {
		t.Fatal("uploaded bytes mismatch")
	}
	if len(dev.commands) == 0 || dev.commands[len(dev.commands)-1] != "app_launch demo.kcapp" {
		t.Fatalf("expected app_launch, got %v", dev.commands)
	}

	// Reject base64 ELF upload.
	r, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "app_upload",
		Arguments: AppUploadArgs{
			Filename:   "evil.kcapp",
			DataBase64: base64.StdEncoding.EncodeToString([]byte("\x7fELF\x01\x01\x01\x00")),
		},
	})
	if err != nil || !r.IsError {
		t.Fatalf("expected error on ELF upload, got %v %+v", err, r)
	}

	// Exercise console tail, list, stop, remove, and path-traversal rejection.
	r, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "app_console", Arguments: AppConsoleArgs{Cursor: 0}})
	if err != nil || r.IsError {
		t.Fatalf("app_console failed: %v %+v", err, r)
	}
	r, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "app_list", Arguments: Empty{}})
	if err != nil || r.IsError {
		t.Fatalf("app_list failed: %v %+v", err, r)
	}
	r, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "app_stop", Arguments: Empty{}})
	if err != nil || r.IsError {
		t.Fatalf("app_stop failed: %v %+v", err, r)
	}
	r, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "app_remove", Arguments: AppNameArgs{Filename: "demo.kcapp"}})
	if err != nil || r.IsError || len(dev.apps) != 0 {
		t.Fatalf("app_remove failed: %v %+v", err, r)
	}
	r, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "app_launch", Arguments: AppNameArgs{Filename: "../escape.kcapp"}})
	if err != nil || !r.IsError {
		t.Fatalf("traversal launch should fail: %v %+v", err, r)
	}
}

func TestVSAAndGPSMCPTools(t *testing.T) {
	ctx := context.Background()
	dev := new(fakeDevice)
	cs, cleanup := connectTestClient(t, ctx, dev)
	defer cleanup()

	freq := 433920000.0
	symRate := 100000.0
	alpha := 0.35
	syms := 512
	preset := 1
	port := 2
	tz := 32

	r, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "vsa_configure",
		Arguments: VSAConfigureArgs{
			Enter:        true,
			Preset:       &preset,
			Port:         &port,
			FrequencyHz:  &freq,
			Modulation:   "16qam",
			SymbolRateHz: &symRate,
			Filter:       "rrc",
			Alpha:        &alpha,
			Equalizer:    "adaptive",
			Symbols:      &syms,
		},
	})
	if err != nil || r.IsError {
		t.Fatalf("vsa_configure failed: %v %+v", err, r)
	}

	// Reject invalid VSA configuration inputs.
	badSyms := 300
	r, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "vsa_configure",
		Arguments: VSAConfigureArgs{Symbols: &badSyms},
	})
	if err != nil || !r.IsError {
		t.Fatalf("expected error on bad VSA symbols: %v %+v", err, r)
	}

	vsaCases := []struct {
		name    string
		args    any
		command string
		fail    bool
	}{
		{"vsa_control", VSAControlArgs{Operation: "single"}, "action vsa_single", false},
		{"vsa_control", VSAControlArgs{Operation: "run"}, "action run", false},
		{"vsa_control", VSAControlArgs{Operation: "clear_persist"}, "action vsa_clear_persist", false},
		{"vsa_control", VSAControlArgs{Operation: "view", View: "evm"}, "action vsa_view 2", false},
		{"vsa_control", VSAControlArgs{Operation: "view", View: "badview"}, "", true},
		{"vsa_status", Empty{}, "vsa", false},
		{"vsa_read", VSAReadArgs{Section: "constellation"}, "vsa_frame constellation", false},
		{"vsa_read", VSAReadArgs{}, "vsa_frame all", false},
		{"vsa_read", VSAReadArgs{Section: "unknown"}, "", true},
		{"vsa_control", VSAControlArgs{Operation: "stop"}, "action stop", false},
		{"gps_control", GPSControlArgs{Operation: "start", OpenPage: true}, "action gps_start 1", false},
		{"gps_status", Empty{}, "gps", false},
		{"gps_track", GPSTrackArgs{Offset: 0, Limit: 32}, "gps_track 0 32", false},
		{"gps_track", GPSTrackArgs{Offset: 0, Limit: 250}, "", true},
		{"gps_control", GPSControlArgs{Operation: "sync_location"}, "action gps_sync_location", false},
		{"gps_control", GPSControlArgs{Operation: "sync_clock"}, "action gps_sync_clock", false},
		{"gps_control", GPSControlArgs{Operation: "sync_clock", TimezoneQuarters: &tz}, "action gps_sync_clock 32", false},
		{"gps_control", GPSControlArgs{Operation: "track_save"}, "action gps_track_save", false},
		{"gps_control", GPSControlArgs{Operation: "track_reset"}, "action gps_track_reset", false},
		{"gps_control", GPSControlArgs{Operation: "stop"}, "action gps_stop", false},
		{"gps_control", GPSControlArgs{Operation: "unknown"}, "", true},
	}

	for _, tc := range vsaCases {
		res, callErr := cs.CallTool(ctx, &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
		if callErr != nil {
			t.Fatal(callErr)
		}
		if res.IsError != tc.fail {
			t.Fatalf("%s (%+v) error=%v content=%v", tc.name, tc.args, res.IsError, res.Content)
		}
		if tc.command != "" {
			dev.mu.Lock()
			last := dev.commands[len(dev.commands)-1]
			dev.mu.Unlock()
			if last != tc.command {
				t.Fatalf("%s: got %q, want %q", tc.name, last, tc.command)
			}
		}
	}
}
