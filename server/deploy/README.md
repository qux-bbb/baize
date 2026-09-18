# Baize (白泽) 部署指南

> 最少操作版：分步 + 预估耗时 + 每步验证。示例参数（如 `192.168.1.10`）**替换为你自己的**。

## 架构与前置条件

```
┌──────────────┐   TLS (gRPC 50051)   ┌──────────────┐
│  Server 机器  │ ◄─────────────────── │  Agent 机器   │
│  gRPC 50051   │  单向TLS + 身份密钥    │  Windows 10+  │
│  HTTPS 8080   │ ───────────────────► │  MSI/zip 安装  │
└──────────────┘   HTTPS (Dashboard)   └──────────────┘
  注册：Agent 凭 enrollment token（下载页生成，可吊销）注册换取
  身份密钥 client.key；token 吊销 = 禁止新注册，Agent 吊销 = 踢下线
```

- 两机同一局域网、可互通
- Server 机器：Windows 10+ 或 Linux（Ubuntu 22.04+ 等）
- Agent 机器：Windows 10+ x64
- 准备：编译好的 `baize-server`（本指南第 1 步）、Agent 的 `baize-agent.exe` 与 `baize-agent.msi`（MSI 在 Agent 构建机上产出，构建方法见第 3 步）
- 注册 token：从 Server Dashboard 下载页生成（可重复使用；泄露可吊销，已注册 Agent 不受影响）

---

## ⚡ Server 安装（GitHub Release，Linux，约 1 分钟）

**方式一：一条命令安装**（目标机可访问 `github.com`）

> 若发布包已发布到 GitHub（`build_release.sh` 加 `BAIZE_RELEASE=1`），目标 Linux 机器可一条命令装完：

```bash
curl -fsSL https://raw.githubusercontent.com/qux-bbb/baize/main/install.sh | sudo bash
```

- 脚本自动：探测 latest release → 下载 tar.gz → SHA256 校验 → 解压 → 调 `install_server.sh` 完成安装
- 指定 Server 局域网地址（免交互，证书 SAN + Agent 连接地址）：

  ```bash
  curl -fsSL https://raw.githubusercontent.com/qux-bbb/baize/main/install.sh | sudo bash -s -- --public-addr 192.168.1.10
  ```

  > ⚠️ 用 `bash -s --` 位置参数传地址/端口（`--port`/`--http-port` 同理）。
  > 不要用环境变量前缀（`VAR=x curl | sudo bash` 只到 curl、且 `sudo` 默认 `env_reset` 会清空自定义环境变量，bash 都拿不到）。

- 不指定地址则 `install_server.sh` 自动检测本机 IP 供选择（多一次交互）
- 指定安装版本（默认 latest release；变量须挂在管道右侧 `bash` 上，root 执行）：

  ```bash
  curl -fsSL https://raw.githubusercontent.com/qux-bbb/baize/main/install.sh | BAIZE_VERSION=0.1.6 bash
  ```

- 前提：目标机可访问 `github.com`（raw + release 下载）

**方式二：手动下载安装**（离线/内网，或指定版本）

1. 到 [Releases 页面](https://github.com/qux-bbb/baize/releases/latest) 下载 `baize-server-<版本>-linux-amd64.tar.gz`（同页 `SHA256SUMS.txt` 用于校验）
2. 校验、解压、安装：

```bash
sha256sum -c --ignore-missing SHA256SUMS.txt
tar xzf baize-server-<版本>-linux-amd64.tar.gz
cd baize-server-<版本>-linux-amd64
sudo ./install_server.sh --public-addr 192.168.1.10
```

- `--public-addr` 可省略：安装脚本自动检测本机 IP 供选择；可选 `--port`（默认 50051）、`--http-port`（默认 8080）
- 后续流程与方式一相同：创建目录/运行用户 → 部署 Agent 分发文件 → 生成 systemd 服务并启动 → 打印**首次登录密码**

---

## 一、Server 部署（Windows，约 10 分钟）

**1. 编译 Server**（开发机，已有 Go 1.22+）：

```cmd
cd server
go build -o baize-server.exe ./cmd/
```

**2. 启动**（Server 机器，管理员 cmd；`192.168.1.10` 替换为 Server 机器的局域网 IP，`ipconfig` 查询）：

```cmd
D:\baize\baize-server.exe --data-dir D:\baize\data --public-addr http://192.168.1.10:50051 --agent-binary D:\baize\baize-agent.exe --agent-installer D:\baize\baize-agent.msi
```

- `--public-addr` **必须**配置：Agent 连接地址与 TLS 证书 SAN 都依赖它
- 首次启动自动生成证书（`data\ca.crt` 等）与登录密码

**3. 防火墙放行**：

```cmd
netsh advfirewall firewall add rule name="Baize gRPC" dir=in action=allow protocol=TCP localport=50051
netsh advfirewall firewall add rule name="Baize HTTPS" dir=in action=allow protocol=TCP localport=8080
```

**✅ 验证**：日志出现 `[TLS] 证书已生成`；`dir D:\baize\data\ca.crt` 存在；记录日志中的**首次密码**。

---

## 二、Server 部署（Linux + systemd，约 10 分钟）

**1. 交叉编译**（开发机）：

```bash
cd server
GOOS=linux GOARCH=amd64 go build -o baize-server ./cmd/
```

**2. 安装**（Linux 机器 root）：

```bash
# 目录与运行用户
mkdir -p /opt/baize/data /opt/baize/agent-files
useradd --system --no-create-home --home-dir /opt/baize baize   # 已存在则跳过

# 拷贝二进制与 Agent 分发文件（Server 只做文件分发，不执行 .exe/.msi）
cp baize-server /opt/baize/
cp baize-agent.exe baize-agent.msi /opt/baize/agent-files/

# 权限与单元文件
chown -R baize:baize /opt/baize
chmod 755 /opt/baize/baize-server
cp baize-server.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now baize-server
```

**3. 修改单元文件中的 IP**（`/etc/systemd/system/baize-server.service` 的 `--public-addr`，替换为 Server 机器的局域网 IP）后 `systemctl daemon-reload && systemctl restart baize-server`。

**✅ 验证**：

```bash
systemctl status baize-server        # active (running)
ls /opt/baize/data/                  # ca.crt / ca.key / server.crt 已生成
ss -tlnp | grep -E '50051|8080'      # 端口监听
curl -sk https://127.0.0.1:8080/api/health   # 返回 ok 类响应
journalctl -u baize-server -n 30     # 首次密码在日志中
```

---

## 三、构建 Agent 安装包（约 5 分钟，Agent 构建机）

MSI 需要内置 Server 的 CA 证书（每台 Server 的 CA 不同，MSI 按 Server 实例构建一次，可分发到多台 Agent；CA 是公钥，随包公开无害）：

```cmd
:: 1. 从 Server 的 data 目录拷贝 CA 证书到 wix 目录
copy /y D:\baize\data\ca.crt D:\files\projects\Baize\agent\wix\ca.crt

:: 2. 构建 MSI（构建输出必须出现"找到 ca.crt，MSI 将内置 TLS CA 证书"）
cd D:\files\projects\Baize\agent\wix
build_msi.bat ..\target\debug\baize-agent.exe
```

**✅ 验证**：构建输出含 `找到 ca.crt`；MSI 拷到 Server 机器（供 `--agent-installer` 与 Dashboard 下载用）。

> **注册 token 不要构建进 MSI**（会随包泄露）：token 一律安装时传 `BAIZE_ENROLLMENT_TOKEN`，从 Dashboard 下载页获取。
>
> 不构建 MSI 也可以：Server 的 Dashboard（https://ServerIP:8080 → 下载页）可直接下载 zip 包（通用包，含 ca.crt；不含 Server 地址与身份），复制一条安装命令到目标机执行即可，zip 方式适合单台快速安装。

---

## 四、Agent 机器安装（约 5 分钟）

**方式 A：一条命令（推荐，目标机 PowerShell，脚本自动提权）**——先从 Dashboard 下载页生成/复制注册 token：

```powershell
curl.exe -k -o baize.zip "https://192.168.1.10:8080/api/agent/package"
Expand-Archive baize.zip -Force
.\baize\install.bat https://192.168.1.10:50051 <注册token>
```

**方式 B：MSI 静默安装**（包已拷贝到目标机，管理员 cmd）：

```cmd
msiexec /i baize-agent.msi /q SERVER_ADDR="https://192.168.1.10:50051" BAIZE_ENROLLMENT_TOKEN="<注册token>"
```

> `192.168.1.10` 必须与 Server 的 `--public-addr` **完全一致**（同 IP、https 前缀）——不一致会导致 TLS 证书验证失败。
> Agent 首次启动自动注册（写入 client.key）；缺 token 或 token 被吊销则注册失败，Agent 会持续重试并在日志提示。

**✅ 验证**：

```cmd
sc query baize-agent        :: 应为 RUNNING
type "C:\Program Files\Baize\agent.conf"   :: server=https://192.168.1.10:50051, ca=ca.crt
type "C:\Program Files\Baize\client.key"   :: 已注册（身份密钥，勿泄露）
powershell -Command "Get-Content C:\ProgramData\Baize\agent.log -Tail 15 | Select-String 'TLS|已连接|注册'"
:: 应见：已启用 TLS, CA: ... + Agent 注册成功 + 已连接到 Server: https://192.168.1.10:50051
```

**Agent 卸载**（设置→应用→Baize Agent，或运行 `C:\Program Files\Baize\uninstall.bat`；脚本自动提权）：

```
uninstall.bat [/S]
:: 停止并删除服务 → 恢复审计策略 → 移除控制面板卸载入口 → 可选删除 C:\Program Files\Baize 与 C:\ProgramData\Baize
:: /S 静默模式（控制面板卸载自动携带，跳过交互确认）
```

---

## 四·补、本机快速验证（前台模式，不装服务，约 3 分钟）

不想装 Windows 服务时，用前台模式验证注册链路（Agent 直接跑在终端里）：

**1. 准备 Agent 目录**（git-bash）：

```bash
mkdir -p /d/baize/agent-v && cd /d/baize/agent-v
cp /d/baize/agent/target/debug/baize-agent.exe .
cp /d/baize/server/data/ca.crt ca.crt         # 换成你的 data 目录路径
printf '{\n  "server": "https://127.0.0.1:50051",\n  "ca": "ca.crt",\n  "watch_dirs": []\n}' > agent.conf
printf '<注册token>' > authd.pass              # token 从 Server 日志或 Dashboard 下载页获取
```

**2. 前台运行**：

```bash
./baize-agent.exe
```

**3. 预期日志**（按顺序）：

```
无 client.key，正在向 Server 注册... → Agent 注册成功 → client.key 已保存
→ 已连接到 Server → 双向流已建立 → 收到 Server 指令（filewatch-init / et-...）
```

**4. 验证**：

```bash
ls agent-v/client.key            # 身份密钥已生成（之后启动直接复用，不再注册）
cat server/data/agents.json      # 注册记录（agent_id / hostname / token_id）
```

> 前台模式以普通用户权限运行，写 `C:\ProgramData\Baize\agent.log` 会报"打开日志文件失败"——这是权限问题（服务模式以 SYSTEM 运行无此问题），日志会 fallback 到终端输出，不影响功能。

---

## 五、验证闭环（约 2 分钟）

1. Server 机器浏览器访问 `https://192.168.1.10:8080`（自签证书警告 → 高级 → 继续访问）→ 用首次密码登录 → 修改密码
2. 下载页生成注册 token（可命名，如"测试机"）→ 复制安装命令到 Agent 机器执行
3. Agent 机器开一个 notepad
4. 主机页应看到 Agent 在线，事件页出现 notepad 的进程创建事件

---

## 六、常见问题排查

| 现象 | 原因 | 处理 |
|---|---|---|
| Agent 日志 `已连接到 Server` 但随后连接被取消/关闭 | TLS 握手失败：地址与 `--public-addr` 不一致，或 ca.crt 未配置 | 核对地址完全一致；`agent.conf` 的 `ca` 应为 `ca.crt` |
| `agent.conf` 的 `ca` 为空 | MSI 构建时 `wix\ca.crt` 不存在（构建输出为"未找到 ca.crt"） | 重新拷贝 ca.crt 并重建 MSI 后重装 |
| Agent 连不上 Server | 防火墙未放行 50051 | Server 机器放行入站 50051/8080 |
| Agent 日志反复"未找到注册 token" | 安装时没传 token（authd.pass 缺失） | 重新执行 `install.bat <地址> <token>` 或手动写 `C:\\Program Files\\Baize\\authd.pass` 后重启服务 |
| Agent 日志"注册 token 无效或已吊销" | token 被吊销/输错 | 下载页重新生成 token，重新安装 |
| Agent 日志"未注册的 Agent"（连接被拒） | client.key 失效或未注册 | 删除 `C:\\Program Files\\Baize\\client.key` 后重启服务（会自动用 token 重新注册）；若仍失败检查 token |
| 服务安装失败 1603 | 旧版本冲突 / 服务被占用 | 看 `msiexec /l*v` 日志；先 `net stop baize-agent` 再重装 |
| 事件页没有数据 | 事件类型开关默认只开 process；无操作产生事件 | 先开 notepad 等程序验证 process；其他事件类型在设置页按需开启 |
| 证书过期 | 自签 CA 与 Server 证书有效期 10 年 | 删除 `data\ca.crt/ca.key/server.crt/server.key` 重启 Server 重新生成，**Agent 需重装**（ca 变更） |
| Agent 报 `certificate not valid yet`（TLS 握手失败） | **Agent 机器系统时间错误**（比证书生效时间早，TLS 严格校验时钟） | Agent 机器同步时间（设置 → 时间 → 自动设置），无需重启服务（15 秒自动重连） |

---

## 网络隔离（隔离 / 解除主机）

Dashboard 主机页行尾菜单或主机详情页可**隔离主机 / 解除隔离**。隔离由 Agent 用 WFP 子层实现：**不改系统防火墙配置**，因此不受 GPO 刷新影响。

**隔离期间保留**：Agent → Server gRPC 端口、DNS(UDP/TCP 53)、本地回环；其余出站全部阻断（含内网横向）。

**状态与自动解除**

- 主机状态列显示「已隔离」（由 Agent 心跳上报，30 秒一轮校正）
- 隔离在 Agent 崩溃、系统重启后保持（过滤器常驻 + 启动时按本地 `isolate.json` 重新施加）
- 隔离时可指定自动解除时间（最长 7 天）；不指定则只能手动解除

**失联时的本地救援**（Agent 连不回 Server、Dashboard 解除不生效）

```cmd
"C:\Program Files\Baize\baize-agent.exe" --isolate-off
```

需管理员权限；只删除 Baize 自己的隔离规则，不影响其它防火墙配置。

**排查残留 / 确认隔离是否生效**

```cmd
netsh wfp show state file=-
```

输出中搜索 `Baize` 子层：隔离生效时应能搜到，解除隔离或卸载 Agent 后应搜不到。

**限制**：仅 Windows Agent 支持；隔离期间主机仍可与 Server 通信（这是设计目标，便于继续取证与下达解除）。

## 安全说明

- **传输**：gRPC 单向 TLS（Agent 内置 CA 验证 Server 身份）+ token 认证 Agent；Dashboard/API 为 HTTPS。对标 Elastic Fleet 默认方案。
- **证书**：Server 首次启动自动生成（自签 CA + Server 证书，SAN 含 localhost/本机 IP/`--public-addr`）；CA 有效期 10 年，过期需重新生成并重装 Agent。
- **默认事件**：仅进程事件（process）开箱启用；network/DNS/file 采集器已实现但默认关闭，按需在设置页开启（network/DNS 事件量大，本地存储需注意容量）。

---

## 附录：服务端一键安装（Linux）

免去手工建目录/建用户/写 systemd 配置的步骤，一条命令完成安装（自动生成单元文件、启动服务、打印首次密码）。

**前提**：`baize-server` 与 `install_server.sh` 同目录。**发布包（build_release.sh 产出）已内置 `baize-agent.exe`**（解压后脚本自动部署为下载分发源，zip 下载立即可用）；`baize-agent.msi` 不放（MSI 内置的 ca.crt 与当前 Server 不匹配，需按本 Server 重建，见第 3 步）。

```bash
sudo ./install_server.sh --public-addr 192.168.1.10
```

**参数**（`--public-addr` 必填，`192.168.1.10` 替换为你自己的 Server IP）：

| 参数 | 默认 | 说明 |
|---|---|---|
| `--public-addr` | **必填** | Server 局域网 IP 或域名（不带 http:// 和端口） |
| `--port` | 50051 | gRPC 端口 |
| `--http-port` | 8080 | Dashboard HTTPS 端口 |
| `--data-dir` | /opt/baize/data | 数据目录 |
| `--agent-dir` | /opt/baize/agent-files | Agent 分发文件目录 |

**脚本自动完成**：创建目录与 `baize` 系统用户 → 拷贝二进制（及 agent 分发文件）→ 生成并写入 `/etc/systemd/system/baize-server.service`（参数已填充）→ 授权 → `systemctl enable --now` 启动 → 健康检查 → 从日志提取首次密码打印。

**验证**：

```bash
systemctl status baize-server        # active (running)
curl -sk https://127.0.0.1:8080/api/health
journalctl -u baize-server -n 30     # 首次密码（脚本已自动打印）
```

**卸载**（`install_server.sh` 会把卸载脚本一并装到 `/opt/baize/`，卸载入口随服务常驻，**无需保留发布包目录**；默认**保留数据目录**，`--purge` 才彻底删除）：

```bash
sudo /opt/baize/uninstall_server.sh            # 交互确认，保留数据
sudo /opt/baize/uninstall_server.sh --purge    # 彻底卸载（连同数据目录、运行用户 baize）
sudo /opt/baize/uninstall_server.sh --yes      # 跳过交互确认（自动化场景）
```

| 参数 | 默认 | 说明 |
|---|---|---|
| `--purge` | 关 | 同时删除数据目录与运行用户 baize（不可恢复） |
| `--yes` / `-y` | 关 | 跳过交互确认 |
| `--data-dir` | /opt/baize/data | 数据目录（须与安装时一致，默认保留） |
| `--agent-dir` | /opt/baize/agent-files | Agent 分发目录 |

**脚本自动完成**：停止并禁用服务（轮询确认停止）→ 删除 systemd 单元并 daemon-reload → 删除程序文件与 Agent 分发文件（保留数据目录，除非 `--purge`）→ 打印防火墙清理提示。幂等：重复执行或未安装时提示后退出，无副作用。

**发布包构建**（本地打包）：

```bash
cd server
./deploy/build_release.sh            # 产出 release/ 下 tar.gz + zip + SHA256SUMS
```

**发布到 GitHub**：

```bash
cd server
./deploy/build_release.sh 0.1.x      # 构建 + 上传 Release 资产（BAIZE_RELEASE=1 触发发布段）
```

GitHub 仓库（`qux-bbb/baize`）为**源码与发布资产同仓库**：`install.sh`（curl 一键安装入口）与 `README.md`
就是仓库根的那一份，随源码提交、改了直接 push，无需另跑同步脚本。
仓库 description 在 GitHub 网页维护（改定位文案时顺手同步一次）。

**重复执行**：脚本幂等——覆盖 systemd 配置并重启服务，以最新参数为准。
