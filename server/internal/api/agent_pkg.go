// Agent 安装包下载 — Server 将 agent.exe + agent.conf + install.bat 打包为 zip 供下载
//
// 说明：
//   - agent.exe 由 --agent-binary 指向（推荐把 agent 二进制放到 Server 机器固定目录，
//     升级 Agent 只需替换文件，无需重新编译 Server）
//   - agent.conf 在下载时动态生成，注入 Server 对外地址（--public-addr 优先，
//     否则用请求 Host 推断），TLS 上线后只需在此处加 ca 字段
//   - install.bat 使用 embed 模板（与 agent/install.bat 保持一致）
package api

import (
	"archive/zip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed agent_install.bat
var installBatFS embed.FS

//go:embed agent_uninstall.bat
var uninstallBatFS embed.FS

// AddrOption 下载页「Server 地址」下拉的一个候选地址
type AddrOption struct {
	Addr   string `json:"addr"`   // 完整 gRPC 地址，TLS 启用时已是 https://
	Source string `json:"source"` // 来源：public（--public-addr）/ host（请求 Host）/ local（本机网卡）/ loopback（同机测试）
}

// AgentInfo 下载页展示的 Agent 打包信息
type AgentInfo struct {
	PublicAddr      string       `json:"public_addr"`       // 将注入 agent.conf 的 Server 地址
	AddrOptions     []AddrOption `json:"addr_options"`      // Server 地址候选（顺序即推荐顺序，前端默认选第一项）
	AgentBinary     string       `json:"agent_binary"`      // --agent-binary 配置的路径
	BinaryExists    bool         `json:"binary_exists"`     // agent 二进制是否就绪
	BinarySize      int64        `json:"binary_size"`       // 字节
	BinarySHA256    string       `json:"binary_sha256"`     // agent 二进制 SHA256
	InstallerPath   string       `json:"installer_path"`    // --agent-installer 配置的 MSI 路径
	InstallerExists bool         `json:"installer_exists"`  // MSI 是否就绪
	InstallerSize   int64        `json:"installer_size"`    // 字节
	InstallerSHA256 string       `json:"installer_sha256"`  // MSI SHA256
	TLSEnabled      bool         `json:"tls_enabled"`       // 是否启用 TLS（ca.crt 就绪，注入包含 ca）
}

// AgentInfo 返回下载页所需的打包信息（GET /api/agent/info）
func (h *Handler) AgentInfo(w http.ResponseWriter, r *http.Request) {
	info := AgentInfo{
		PublicAddr:    h.agentServerAddr(h.resolvePublicAddr(r)),
		AddrOptions:   h.addrCandidates(r),
		AgentBinary:   h.agentBinary,
		InstallerPath: h.agentInstaller,
		TLSEnabled:    h.caFile != "",
	}
	if h.agentBinary != "" {
		if fi, err := os.Stat(h.agentBinary); err == nil && !fi.IsDir() {
			info.BinaryExists = true
			info.BinarySize = fi.Size()
			if sum, err := fileSHA256(h.agentBinary); err == nil {
				info.BinarySHA256 = sum
			}
		}
	}
	if h.agentInstaller != "" {
		if fi, err := os.Stat(h.agentInstaller); err == nil && !fi.IsDir() {
			info.InstallerExists = true
			info.InstallerSize = fi.Size()
			if sum, err := fileSHA256(h.agentInstaller); err == nil {
				info.InstallerSHA256 = sum
			}
		}
	}
	json.NewEncoder(w).Encode(info)
}

// AgentPackage 打包下载（GET /api/agent/package，公开下载）
// zip 内容：baize-agent.exe + agent.conf + install.bat + ca.crt + SHA256SUMS.txt
//
// 通用包模型（对标 Wazuh packages.wazuh.com）：
//   - agent.conf 不含 Server 地址（安装时由 install.bat/MSI 写入）
//   - ca.crt 随包分发（CA 公钥，无敏感信息；供 Agent 验证 Server TLS）
//   - 身份通过 enrollment token 注册获取（见 Register RPC）
func (h *Handler) AgentPackage(w http.ResponseWriter, r *http.Request) {
	if h.agentBinary == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "服务端未配置 --agent-binary，无法打包"})
		return
	}
	exe, err := os.Open(h.agentBinary)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法读取 agent 二进制: " + err.Error()})
		return
	}
	defer exe.Close()

	// 通用配置模板：server 留空（安装时填写），ca 指向包内 ca.crt（TLS 公钥随包）
	ca := ""
	if h.caFile != "" {
		ca = "ca.crt"
	}
	conf, _ := json.MarshalIndent(map[string]interface{}{
		"server":     "",
		"ca":         ca,
		"watch_dirs": []string{},
	}, "", "  ")

	installBat, err := installBatFS.ReadFile("agent_install.bat")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "install.bat 模板缺失"})
		return
	}

	filename := fmt.Sprintf("baize-agent_%s.zip", time.Now().Format("20060102_150405"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	zw := zip.NewWriter(w)
	if err := addZipReader(zw, "baize-agent.exe", exe); err != nil {
		zw.Close()
		return
	}
	if err := addZipBytes(zw, "agent.conf", conf); err != nil {
		zw.Close()
		return
	}
	if err := addZipBytes(zw, "install.bat", installBat); err != nil {
		zw.Close()
		return
	}
	uninstallBat, err := uninstallBatFS.ReadFile("agent_uninstall.bat")
	if err != nil {
		zw.Close()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "uninstall.bat 模板缺失"})
		return
	}
	if err := addZipBytes(zw, "uninstall.bat", uninstallBat); err != nil {
		zw.Close()
		return
	}
	if h.caFile != "" {
		caData, err := os.ReadFile(h.caFile)
		if err != nil {
			zw.Close()
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "读取 CA 证书失败: " + err.Error()})
			return
		}
		if err := addZipBytes(zw, "ca.crt", caData); err != nil {
			zw.Close()
			return
		}
	}

	// SHA256SUMS.txt：安装前可校验文件完整性
	exeSum, _ := fileSHA256(h.agentBinary)
	shaLines := fmt.Sprintf("baize-agent.exe  %s\n", exeSum)
	shaLines += fmt.Sprintf("agent.conf       %s\n", sha256hex(conf))
	shaLines += fmt.Sprintf("install.bat      %s\n", sha256hex(installBat))
	shaLines += fmt.Sprintf("uninstall.bat    %s\n", sha256hex(uninstallBat))
	if h.caFile != "" {
		if caSum, err := fileSHA256(h.caFile); err == nil {
			shaLines += fmt.Sprintf("ca.crt           %s\n", caSum)
		}
	}
	_ = addZipBytes(zw, "SHA256SUMS.txt", []byte(shaLines))

	if err := zw.Close(); err != nil {
		return
	}
}

// AgentInstaller 下发预构建的 MSI 安装包（GET /api/agent/installer，需 JWT）
func (h *Handler) AgentInstaller(w http.ResponseWriter, r *http.Request) {
	if h.agentInstaller == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "服务端未配置 --agent-installer，无法下发 MSI"})
		return
	}
	f, err := os.Open(h.agentInstaller)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法读取 MSI: " + err.Error()})
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "MSI 文件无效"})
		return
	}

	filename := filepath.Base(h.agentInstaller)
	w.Header().Set("Content-Type", "application/x-msi")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
	http.ServeContent(w, r, filename, fi.ModTime(), f)
}

// agentServerAddr: TLS 启用时把注入地址的 http:// 转 https://
// （Agent 侧根据 server 前缀决定是否启用 TLS，ca 指向包内 ca.crt）
func (h *Handler) agentServerAddr(addr string) string {
	if h.caFile == "" || !strings.HasPrefix(addr, "http://") {
		return addr
	}
	return "https://" + strings.TrimPrefix(addr, "http://")
}

// hostOnly 从 Host 头（可能带端口、可能是 [IPv6] 形式）取出主机名
func hostOnly(hostport string) string {
	host := hostport
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(strings.TrimSpace(host), "[]") // 去掉 IPv6 方括号
}

// resolvePublicAddr 确定 agent.conf 注入的 Server 地址：
// --public-addr 优先（完整 gRPC 地址，如 http://10.0.0.1:50051）；
// 未配置时用请求 Host 的 hostname 拼 http://<host>:<gRPC 端口>
func (h *Handler) resolvePublicAddr(r *http.Request) string {
	if h.publicAddr != "" {
		return h.publicAddr
	}
	if r == nil {
		return ""
	}
	host := hostOnly(r.Host)
	if host == "" {
		return ""
	}
	return fmt.Sprintf("http://%s:%d", host, h.grpcPort)
}

// addrCandidates 返回下载页「Server 地址」下拉的全部候选，顺序即推荐顺序：
//  1. public   —— --public-addr（Server 显式声明的对外地址，最可信）
//  2. host     —— 请求 Host 推断（Dashboard 当前正在用的地址）
//  3. local    —— 本机各非回环 IPv4 网卡
//  4. loopback —— 127.0.0.1（仅同机测试用，排在最后避免误发给远程机）
//
// 取址口径与证书 SAN（见 cmd/tls.go）一致：都是 --public-addr + 本机非回环 IPv4 + 回环，
// 所以正常情况下 TLS 下都能通过 Agent 侧证书校验；IPv6 与任意外部域名不在 SAN 内，
// 一律不进候选。注意证书存在即复用，改了 --public-addr 或换了网卡后需删掉
// data-dir 的证书重新生成，否则 SAN 仍是旧的（该隐患与本次改动无关）。
// 去重按最终地址（TLS 转换后）大小写不敏感判定。
func (h *Handler) addrCandidates(r *http.Request) []AddrOption {
	var out []AddrOption
	seen := map[string]bool{}
	add := func(raw, source string) {
		addr := h.agentServerAddr(strings.TrimRight(strings.TrimSpace(raw), "/"))
		if addr == "" {
			return
		}
		key := strings.ToLower(addr)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, AddrOption{Addr: addr, Source: source})
	}

	if h.publicAddr != "" {
		add(h.publicAddr, "public")
	}
	if r != nil {
		if host := hostOnly(r.Host); host != "" {
			add(fmt.Sprintf("http://%s:%d", host, h.grpcPort), "host")
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil || ipn.IP.IsLoopback() {
				continue // 只取非回环 IPv4；IPv6 不在证书 SAN 内，TLS 下不可用
			}
			add(fmt.Sprintf("http://%s:%d", ipn.IP.String(), h.grpcPort), "local")
		}
	}
	add(fmt.Sprintf("http://127.0.0.1:%d", h.grpcPort), "loopback")
	return out
}

// ── zip 写入辅助 ─────────────────────────────────────────

func addZipReader(zw *zip.Writer, name string, r io.Reader) error {
	fw, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = io.Copy(fw, r)
	return err
}

func addZipBytes(zw *zip.Writer, name string, data []byte) error {
	fw, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = fw.Write(data)
	return err
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sha256hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
