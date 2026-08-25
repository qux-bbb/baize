# Baize (白泽)

> 白泽识妖，无所遁形 — 轻量终端安全检测（EDR）

这是一个**发布仓库**：托管一键安装脚本 `install.sh` 与 Release 发布产物。
**源码私有**，不在此仓库。

## 一键安装 Server（Linux）

```bash
curl -fsSL https://raw.githubusercontent.com/qux-bbb/baize/main/install.sh | sudo bash
```

可选环境变量：

| 变量 | 说明 |
|------|------|
| `BAIZE_PUBLIC_ADDR` | Server 局域网地址（证书 SAN + Agent 连接地址）。给了免交互；不给则自动检测选 IP |
| `BAIZE_VERSION` | 指定安装版本（默认 latest release） |
| `BAIZE_OFFLINE_TARBALL` | 本地已有 tar.gz，跳过下载（离线/内网现场） |

> 前提：目标机 Linux + root + `curl` + `tar` + `sha256sum`，且能访问 `github.com`（raw 与 release 下载）。

## Release 产物

`v0.1.x` 发布的 assets：`baize-server-*-linux-amd64.tar.gz` / 对应 `-windows-amd64.zip`、`baize-agent-*-windows-amd64.zip`、`SHA256SUMS.txt`（安装脚本会校验完整性）。

## 协议

GPL-3.0