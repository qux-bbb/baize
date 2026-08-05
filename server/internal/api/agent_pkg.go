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

// AgentInfo 下载页展示的 Agent 打包信息
type AgentInfo struct {
	PublicAddr      string `json:"public_addr"`       // 将注入 agent.conf 的 Server 地址
	AgentBinary     string `json:"agent_binary"`      // --agent-binary 配置的路径
	BinaryExists    bool   `json:"binary_exists"`     // agent 二进制是否就绪
	BinarySize      int64  `json:"binary_size"`       // 字节
	BinarySHA256    string `json:"binary_sha256"`     // agent 二进制 SHA256
	InstallerPath   string `json:"installer_path"`    // --agent-installer 配置的 MSI 路径
	InstallerExists bool   `json:"installer_exists"`  // MSI 是否就绪
	InstallerSize   int64  `json:"installer_size"`    // 字节
	InstallerSHA256 string `json:"installer_sha256"`  // MSI SHA256
}

// AgentInfo 返回下载页所需的打包信息（GET /api/agent/info）
func (h *Handler) AgentInfo(w http.ResponseWriter, r *http.Request) {
	info := AgentInfo{
		PublicAddr:    h.resolvePublicAddr(r),
		AgentBinary:   h.agentBinary,
		InstallerPath: h.agentInstaller,
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

// AgentPackage 打包下载（GET /api/agent/package，需 JWT）
// zip 内容：baize-agent.exe + agent.conf + install.bat + SHA256SUMS.txt
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

	serverAddr := h.resolvePublicAddr(r)
	if serverAddr == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法确定 Server 对外地址，请配置 --public-addr"})
		return
	}

	// 动态生成 agent.conf（ca 字段预留，TLS 上线后填充）
	conf, _ := json.MarshalIndent(map[string]interface{}{
		"server":     serverAddr,
		"ca":         "",
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

	// SHA256SUMS.txt：安装前可校验文件完整性
	exeSum, _ := fileSHA256(h.agentBinary)
	shaLines := fmt.Sprintf("baize-agent.exe  %s\n", exeSum)
	shaLines += fmt.Sprintf("agent.conf       %s\n", sha256hex(conf))
	shaLines += fmt.Sprintf("install.bat      %s\n", sha256hex(installBat))
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

// resolvePublicAddr 确定 agent.conf 注入的 Server 地址：
// --public-addr 优先（完整 gRPC 地址，如 http://10.0.0.1:50051）；
// 未配置时用请求 Host 的 hostname 拼 http://<host>:50051
func (h *Handler) resolvePublicAddr(r *http.Request) string {
	if h.publicAddr != "" {
		return h.publicAddr
	}
	if r == nil {
		return ""
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]") // 去掉 IPv6 括号
	if host == "" {
		return ""
	}
	return fmt.Sprintf("http://%s:50051", host)
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
