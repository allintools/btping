# btping

批量 TCP 端口检测桌面程序，Go + Wails 实现，Windows 优先。

## 功能

- 批量目标：每行一个，支持 `host`、`host:port`、`host port`、`[::1]`、`[::1]:443`，`#` 之后视为注释
- 端口写法：`80,443`、`8000-8010`、`3306 6379`；写在目标行里的端口会覆盖端口输入框
- 多轮探测：统计最近 / 最小 / 平均 / 最大延迟、成功次数与丢包率
- 实时回显：探测结果流式推送，运行中可随时停止
- 结果筛选、按列排序、导出 CSV（带 UTF-8 BOM，Excel 可直接打开中文）
- 输入参数自动记忆，`Ctrl+Enter` 开始，`Esc` 停止

## 状态含义

| 状态 | 含义 |
| --- | --- |
| 开放 | TCP 握手完成，端口可连接 |
| 关闭 | 对端返回 RST，主机在线但端口没有监听 |
| 超时 | 一直没有响应，通常是防火墙丢包 |
| 解析失败 | 域名无法解析 |
| 错误 | 其他网络错误，例如网络不可达 |

## 开发

```bash
wails dev
```

## 构建

```bash
wails build
```

产物位于 `build/bin/btping.exe`，单文件、无需额外运行时（Windows 自带 WebView2）。

## 目录结构

```
main.go                     Wails 窗口与启动参数
app.go                      绑定层：启动/停止检测、事件推送、CSV 导出
internal/scanner/parse.go   目标与端口解析
internal/scanner/run.go     并发探测引擎与统计
frontend/                   界面（原生 HTML/CSS/JS + lucide 图标）
```

## 测试

```bash
go test ./...
```
