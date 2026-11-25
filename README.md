# KTOS MCP Server (`ktos-mcp`)

Go stdio [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server for controlling a KC908 running KTOS custom firmware over USB 3.0. It exposes 33 structured MCP tools for spectrum analysis, vector signal analysis (VSA), software GPS L1 reception/tracking, receiver & signal-source control, live telemetry, SSTV/SD image transfer, and sandboxed `.kcapp` deployment/debugging, plus one-shot CLI flags for diagnostics and app development.

[简体中文](README.zh-CN.md)

## Prerequisites

- Windows x64 (for live USB 3.0 device transport)
- FTDI D3XX driver installed
- 64-bit `kcsdr.dll` (from the KTOS Host SDK) and official 64-bit `FTD3XX.dll` (>= 1.3.0.4) placed alongside `ktos-mcp.exe` (or passed via `--sdk`)
- KC908 switched to **Custom System → Remote → MCP Mode**

## Building and Testing

Requires Go 1.26+.

```powershell
# Run unit tests (in-memory MCP transport; no hardware required)
go test ./...

# Build Windows x64 binary
go build -trimpath -o ktos-mcp.exe ./cmd/ktos-mcp
```

## MCP Client Configuration

Place `ktos-mcp.exe`, `kcsdr.dll`, and `FTD3XX.dll` in the same directory and add the server to your MCP client configuration:

```json
{
  "mcpServers": {
    "kc908": {
      "command": "C:/KTOS/x64/ktos-mcp.exe",
      "args": []
    }
  }
}
```

Use `--sdk <path>` if `kcsdr.dll` resides in a different directory, and `--device N` to select a non-default FT601 index. The device handle is opened lazily on the first tool call.

## CLI Utilities

In addition to stdio MCP mode, `ktos-mcp.exe` supports direct CLI sub-modes:

```powershell
# Print device capabilities JSON and exit
ktos-mcp.exe --command capabilities

# Upload a .kcapp module, launch it in the sandbox, and stream KC_CONSOLE output
ktos-mcp.exe -app-upload sat_tracker.kcapp -app-run -app-tail

# Stop the foreground KCApp
ktos-mcp.exe -app-stop
```

## Available MCP Tools (33)

| Category | Tools |
| --- | --- |
| Discovery & parameters | `instrument_capabilities`, `instrument_parameters`, `instrument_get` |
| Mode & configuration | `instrument_mode`, `spectrum_configure`, `receiver_configure`, `source_configure`, `instrument_configure` |
| Actions, spectrum & status | `instrument_action`, `spectrum_read`, `instrument_status` |
| Vector signal analysis (VSA) | `vsa_configure`, `vsa_control`, `vsa_status`, `vsa_read` |
| Software GPS L1 | `gps_control`, `gps_status`, `gps_track` |
| UI, text & channels | `instrument_menu`, `instrument_input`, `instrument_text`, `receiver_channel` |
| SD / SSTV image transfer | `image_upload`, `image_download`, `image_remove`, `image_transfer_status` |
| KCApp sandbox management | `app_list`, `app_scan`, `app_upload`, `app_remove`, `app_launch`, `app_stop`, `app_console` |

## Repository Layout

```text
.
├── LICENSE                          # MIT License
├── go.mod / go.sum                  # Go module definition
├── cmd/
│   └── ktos-mcp/
│       ├── main.go                  # Entry point, CLI flags, and KCApp console tailer
│       └── version.go               # Server and binary version constant
├── internal/
│   ├── device/
│   │   ├── device.go                # Device interfaces, thread-safe wrapper, JSON envelope & parameter codec
│   │   ├── native_windows.go        # Windows amd64 kcsdr.dll syscall bridge
│   │   ├── native_other.go          # Non-Windows stub for cross-platform compilation/tests
│   │   └── device_test.go           # Envelope and parameter validation tests
│   ├── transfer/
│   │   ├── client.go                # Binary KIMG packet framing and error mapping
│   │   ├── images.go                # Synchronous image upload/download, file save, BMP preview
│   │   ├── jobs.go                  # Background upload job tracking and cancellation
│   │   ├── apps.go                  # WebAssembly v1 validation and .kcapp transfer
│   │   └── *_test.go                # Transfer protocol, job, and Wasm validation tests
│   └── server/
│       ├── server.go                # MCP server wiring and instrument control tools
│       ├── tools_vsa.go             # Vector signal analysis (VSA) MCP tools
│       ├── tools_gps.go             # Software GPS L1 MCP tools
│       ├── tools_image.go           # SD / SSTV image MCP tools
│       ├── tools_app.go             # KCApp sandbox MCP tools
│       └── server_test.go           # In-memory end-to-end MCP tool tests
└── resources/
    └── version.rc.in                # Windows VERSIONINFO resource template
```

## License

This project is licensed under the [MIT License](LICENSE).
