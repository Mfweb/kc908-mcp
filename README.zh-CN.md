# KTOS MCP 服务（`ktos-mcp`）

基于 Go 编写的 stdio [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) 服务，用于通过 USB 3.0 控制运行 KTOS 自制固件的 KC908 仪器。共提供 26 个结构化 MCP 工具，涵盖频谱仪、接收机、信号源、实时遥测、SD/SSTV 图片传输以及 `.kcapp` 沙盒应用部署与调试，同时支持命令行诊断与一键调试模式。

[English](README.md)

## 运行环境

- Windows x64（用于实机 USB 3.0 通信）
- 已安装 FTDI D3XX 驱动
- 64 位 `kcsdr.dll`（来自 KTOS 主机 SDK）与 64 位官方 `FTD3XX.dll`（1.3.0.4 或更新版本），放在 `ktos-mcp.exe` 同目录（或通过 `--sdk` 指定）
- 设备端开启 **自制系统 → 远程 → MCP 模式**

## 编译与测试

需要 Go 1.26 或更高版本。

```powershell
# 运行离线单元测试（使用内存 MCP 传输，无需连接真机）
go test ./...

# 编译 Windows x64 可执行文件
go build -trimpath -o ktos-mcp.exe ./cmd/ktos-mcp
```

## MCP 客户端配置

将 `ktos-mcp.exe`、`kcsdr.dll`、`FTD3XX.dll` 放在同一目录后，在支持本地 stdio MCP 的 AI 客户端中添加：

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

若 `kcsdr.dll` 位于其他路径，可通过 `--sdk <路径>` 指定；多台设备时可通过 `--device N` 选择 FT601 索引。服务在首次调用工具时自动打开设备，连接中断后下次调用会自动重连，但不会重放失败的写入命令。

## 命令行工具模式

除 stdio MCP 服务外，还支持单次诊断与 KCApp 开发调试命令：

```powershell
# 查询设备能力 JSON 后退出
ktos-mcp.exe --command capabilities

# 上传 .kcapp、在设备沙盒启动并实时输出 KC_CONSOLE 调试日志
ktos-mcp.exe -app-upload sat_tracker.kcapp -app-run -app-tail

# 停止当前前台运行中的 KCApp
ktos-mcp.exe -app-stop
```

## 提供的 MCP 工具（共 26 个）

| 分类 | 工具 |
| --- | --- |
| 能力发现与参数查询 | `instrument_capabilities`、`instrument_parameters`、`instrument_get` |
| 模式切换与参数配置 | `instrument_mode`、`spectrum_configure`、`receiver_configure`、`source_configure`、`instrument_configure` |
| 动作、频谱与遥测状态 | `instrument_action`、`spectrum_read`、`instrument_status` |
| 界面交互、文本与频道 | `instrument_menu`、`instrument_input`、`instrument_text`、`receiver_channel` |
| SD / SSTV 图片传输 | `image_upload`、`image_download`、`image_remove`、`image_transfer_status` |
| KCApp 沙盒应用管理 | `app_list`、`app_scan`、`app_upload`、`app_remove`、`app_launch`、`app_stop`、`app_console` |

## 目录结构

```text
.
├── LICENSE                          # MIT 许可证
├── go.mod / go.sum                  # Go 模块与依赖校验
├── cmd/
│   └── ktos-mcp/
│       ├── main.go                  # 程序入口、CLI 参数与 KCApp 控制台日志拉取
│       └── version.go               # 服务与二进制版本号常量
├── internal/
│   ├── device/
│   │   ├── device.go                # 设备接口、线程安全封装、JSON 响应与参数编解码
│   │   ├── native_windows.go        # Windows amd64 kcsdr.dll 动态调用桥接
│   │   ├── native_other.go          # 非 Windows 平台构建/测试占位实现
│   │   └── device_test.go           # 响应解码与参数校验单元测试
│   ├── transfer/
│   │   ├── client.go                # 二进制 KIMG 传输协议封装与错误映射
│   │   ├── images.go                # 同步图片上传/下载、落盘与 BMP 预览转码
│   │   ├── jobs.go                  # 大图后台上传任务状态管理与取消
│   │   ├── apps.go                  # WebAssembly v1 结构校验与 .kcapp 传输
│   │   └── *_test.go                # 图片/任务/Wasm 校验单元测试
│   └── server/
│       ├── server.go                # MCP 服务初始化与仪器控制工具注册
│       ├── tools_image.go           # SD / SSTV 图片传输 MCP 工具
│       ├── tools_app.go             # KCApp 沙盒管理 MCP 工具
│       └── server_test.go           # 基于内存传输的 26 项 MCP 工具端到端测试
└── resources/
    └── version.rc.in                # Windows 文件版本资源模板
```

## 许可证

本项目基于 [MIT 许可证](LICENSE) 开源。
