// 远程响应操作处理器 — 向在线 Agent 下发指令并同步等待结果
package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	pb "github.com/qux-bbb/baize/proto/gen/go/baize/v1"
	"github.com/qux-bbb/baize/server/internal/engine"
	"github.com/qux-bbb/baize/server/internal/filexfer"
)

// CmdKill 远程终止指定主机上的进程（POST /api/cmd/kill）
// body: {"agent_id": "...", "pid": 1234}
func (h *Handler) CmdKill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID string `json:"agent_id"`
		Pid     uint64 `json:"pid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.AgentID == "" {
		writeErr(w, http.StatusBadRequest, "missing agent_id")
		return
	}
	if req.Pid == 0 {
		writeErr(w, http.StatusBadRequest, "missing pid")
		return
	}

	res, err := h.cmdBus.ExecuteSync(req.AgentID, engine.BuildKillCmd(req.Pid), 30*time.Second)
	if err != nil {
		// 下发失败 / Agent 离线 / 超时
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !res.GetSuccess() {
		writeErr(w, http.StatusInternalServerError, res.GetErrorMessage())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// CmdListDir 列出远程目录（POST /api/cmd/list-dir）
// body: {"agent_id": "...", "path": "C:\\Windows"}
// 返回: {"entries": [{name,path,is_dir,size,modified}]}
func (h *Handler) CmdListDir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID string `json:"agent_id"`
		Path    string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.AgentID == "" {
		writeErr(w, http.StatusBadRequest, "missing agent_id")
		return
	}

	res, err := h.cmdBus.ExecuteSync(req.AgentID, engine.BuildListDirCmd(req.Path), 30*time.Second)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !res.GetSuccess() {
		writeErr(w, http.StatusInternalServerError, res.GetErrorMessage())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"entries": json.RawMessage(res.GetOutput())})
}

// CmdDelete 远程删除文件/目录（POST /api/cmd/delete）
// body: {"agent_id": "...", "path": "...", "recursive": true}
func (h *Handler) CmdDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID   string `json:"agent_id"`
		Path      string `json:"path"`
		Recursive bool   `json:"recursive"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.AgentID == "" || req.Path == "" {
		writeErr(w, http.StatusBadRequest, "missing agent_id/path")
		return
	}

	res, err := h.cmdBus.ExecuteSync(req.AgentID, engine.BuildDeletePathCmd(req.Path, req.Recursive), 30*time.Second)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !res.GetSuccess() {
		writeErr(w, http.StatusInternalServerError, res.GetErrorMessage())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// FileDownload 远程下载文件（GET /api/file/download?agent_id=&path=）
// 真流式：边从 gRPC 传输流收块边写 HTTP 响应。
func (h *Handler) FileDownload(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	path := r.URL.Query().Get("path")
	if agentID == "" || path == "" {
		writeErr(w, http.StatusBadRequest, "missing agent_id/path")
		return
	}

	transferID := filexfer.NewTransferID()
	tr := h.trans.NewDownload(agentID, transferID)
	defer h.trans.Remove(transferID)

	cmd := engine.BuildFileDownloadCmd(transferID, path)
	if err := h.cmdBus.SendToAgent(agentID, cmd); err != nil {
		tr.SetErr(err)
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	filename := filepath.Base(path)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(filename)))
	w.Header().Set("Content-Type", "application/octet-stream")
	flusher, _ := w.(http.Flusher)
	started := false

	for {
		select {
		case chunk, ok := <-tr.Chunks():
			if !ok {
				writeErr(w, http.StatusInternalServerError, "传输通道已关闭")
				return
			}
			if chunk.GetError() != "" {
				// 传输失败：尚未写出任何数据时可返回 JSON 错误
				if !started {
					w.Header().Del("Content-Disposition")
					writeErr(w, http.StatusInternalServerError, chunk.GetError())
				}
				return
			}
			if len(chunk.GetData()) > 0 {
				if _, err := w.Write(chunk.GetData()); err != nil {
					// 浏览器取消下载
					tr.SetErr(fmt.Errorf("客户端取消下载"))
					return
				}
				started = true
				if flusher != nil {
					flusher.Flush()
				}
			}
			if chunk.GetLast() {
				return
			}
		case <-tr.Done():
			// Agent 断开 / 传输被取消
			return
		case <-time.After(filexfer.IdleTimeout):
			return
		}
	}
}

// FileUpload 远程上传文件（POST /api/file/upload?agent_id=&path=&overwrite=1&size=）
// 真流式：边读 HTTP 请求体边经 gRPC 传输流分块发送给 Agent。
func (h *Handler) FileUpload(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	destPath := r.URL.Query().Get("path")
	overwrite := r.URL.Query().Get("overwrite") == "1"
	if agentID == "" || destPath == "" {
		writeErr(w, http.StatusBadRequest, "missing agent_id/path")
		return
	}
	size, _ := strconv.ParseUint(r.URL.Query().Get("size"), 10, 64)

	transferID := filexfer.NewTransferID()
	tr := h.trans.NewUpload(agentID, transferID)
	defer h.trans.Remove(transferID)

	cmd := engine.BuildFileUploadCmd(transferID, destPath, size, overwrite)
	if err := h.cmdBus.SendToAgent(agentID, cmd); err != nil {
		tr.SetErr(err)
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// 等待 Agent 建立传输流
	select {
	case <-tr.Ready():
	case <-tr.Done():
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("传输中断: %v", tr.Err()))
		return
	case <-time.After(filexfer.StreamReadyTimeout):
		tr.SetErr(fmt.Errorf("Agent 未在 %v 内建立传输流", filexfer.StreamReadyTimeout))
		writeErr(w, http.StatusGatewayTimeout, "Agent 未建立传输流，请确认 Agent 在线")
		return
	}

	// 流式读取请求体，逐块发给 Agent
	buf := make([]byte, filexfer.ChunkSize)
	var seq uint64
	for {
		n, err := io.ReadFull(r.Body, buf)
		if n > 0 {
			if sendErr := tr.Send(&pb.FileChunk{TransferId: transferID, Seq: seq, Data: buf[:n]}); sendErr != nil {
				writeErr(w, http.StatusBadGateway, "发送到 Agent 失败: "+sendErr.Error())
				return
			}
			seq++
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			_ = tr.Send(&pb.FileChunk{TransferId: transferID, Seq: seq, Error: err.Error()})
			writeErr(w, http.StatusBadRequest, "读取请求体失败: "+err.Error())
			return
		}
		select {
		case <-tr.Done():
			writeErr(w, http.StatusBadGateway, fmt.Sprintf("传输中断: %v", tr.Err()))
			return
		default:
		}
	}
	if err := tr.Send(&pb.FileChunk{TransferId: transferID, Seq: seq, Last: true}); err != nil {
		writeErr(w, http.StatusBadGateway, "发送结束标记失败: "+err.Error())
		return
	}

	// 等待 Agent 落盘结果（大文件耗时，放宽超时）
	res, err := h.cmdBus.WaitResult(cmd.GetCommandId(), 10*time.Minute)
	if err != nil {
		writeErr(w, http.StatusGatewayTimeout, err.Error())
		return
	}
	if !res.GetSuccess() {
		writeErr(w, http.StatusInternalServerError, res.GetErrorMessage())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── 远程执行命令 ──────────────────────────────────────────

// execInterpreters 支持的脚本解释器白名单（与 Agent execute_script 保持一致）
var execInterpreters = map[string]bool{
	"powershell": true,
	"cmd":        true,
	"bash":       true,
	"python":     true,
}

// execMaxTimeoutSecs 单条命令允许的最大执行超时（秒）
const execMaxTimeoutSecs = 600

// execLogScriptLimit 日志中命令内容的最大显示长度（rune 数）
const execLogScriptLimit = 200

// truncateForLog 截断超长命令，避免日志刷屏（按 rune 截断，不会切坏 UTF-8）
func truncateForLog(s string) string {
	r := []rune(s)
	if len(r) <= execLogScriptLimit {
		return s
	}
	return string(r[:execLogScriptLimit]) + "…(已截断)"
}

// CmdExec 远程执行命令/脚本（POST /api/cmd/exec）
// body: {"agent_id":"...","script":"...","interpreter":"powershell","timeout_secs":30,"reason":"..."}
// 返回: {"success":bool,"output":"...","error_message":"...","elapsed_ms":123}
//
// 错误约定（与 CmdKill 一致，但命令执行失败不算 HTTP 错误）：
//   - 下发失败 / Agent 离线 / 等待超时 → 4xx
//   - 命令本身执行失败（非 0 退出码）→ 200 + success=false + output（前端展示报错输出）
func (h *Handler) CmdExec(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID     string `json:"agent_id"`
		Script      string `json:"script"`
		Interpreter string `json:"interpreter"`
		TimeoutSecs uint32 `json:"timeout_secs"`
		Reason      string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.AgentID == "" {
		writeErr(w, http.StatusBadRequest, "missing agent_id")
		return
	}
	if strings.TrimSpace(req.Script) == "" {
		writeErr(w, http.StatusBadRequest, "missing script")
		return
	}
	if !execInterpreters[req.Interpreter] {
		writeErr(w, http.StatusBadRequest, "unsupported interpreter: "+req.Interpreter)
		return
	}
	if req.TimeoutSecs > execMaxTimeoutSecs {
		req.TimeoutSecs = execMaxTimeoutSecs
	}

	username := UsernameFromContext(r.Context())
	// 审计留痕：谁、对哪台主机、用什么解释器、执行了什么命令、为什么
	log.Printf("[响应] 执行命令 user=%s agent=%s interpreter=%s timeout=%ds reason=%q script=%q",
		username, req.AgentID, req.Interpreter, req.TimeoutSecs, req.Reason, truncateForLog(req.Script))

	// 等待超时 = 命令超时 + 5s 缓冲（timeout_secs=0 时 Agent 侧按默认 30s 执行）
	wait := 30 * time.Second
	if req.TimeoutSecs > 0 {
		wait = time.Duration(req.TimeoutSecs) * time.Second
	}
	wait += 5 * time.Second

	started := time.Now()
	res, err := h.cmdBus.ExecuteSync(req.AgentID, engine.BuildExecCmd(req.Script, req.Interpreter, req.TimeoutSecs, req.Reason), wait)
	elapsed := time.Since(started)
	if err != nil {
		log.Printf("[响应] 执行命令下发失败 user=%s agent=%s 耗时=%v err=%v",
			username, req.AgentID, elapsed.Round(time.Millisecond), err)
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	log.Printf("[响应] 执行命令完成 user=%s agent=%s success=%v error=%q 耗时=%v",
		username, req.AgentID, res.GetSuccess(), res.GetErrorMessage(), elapsed.Round(time.Millisecond))

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":       res.GetSuccess(),
		"output":        res.GetOutput(),
		"error_message": res.GetErrorMessage(),
		"elapsed_ms":    elapsed.Milliseconds(),
	})
}

// ── 网络隔离 ──────────────────────────────────────────────

// isolateMaxTTLSeconds 隔离自动解除倒计时上限（7 天，对标 MDE）
const isolateMaxTTLSeconds = 7 * 24 * 3600

// CmdIsolate 隔离 / 解除隔离主机（POST /api/cmd/isolate）
// body: {"agent_id":"...","action":"isolate"|"release","reason":"...","ttl_seconds":604800}
// 返回: {"status":"ok","isolated":true,"output":"..."}
//
// 隔离由 Agent 端 WFP 子层实现（默认全断 + 管理通道白名单）。
// 与 kill/exec 一致的错误约定：下发失败/Agent 离线/超时 → 4xx；Agent 执行失败 → 5xx + 原因。
func (h *Handler) CmdIsolate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID    string `json:"agent_id"`
		Action     string `json:"action"`
		Reason     string `json:"reason"`
		TTLSeconds uint32 `json:"ttl_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.AgentID == "" {
		writeErr(w, http.StatusBadRequest, "missing agent_id")
		return
	}
	if req.Action != "isolate" && req.Action != "release" {
		writeErr(w, http.StatusBadRequest, "invalid action (isolate|release)")
		return
	}
	isolate := req.Action == "isolate"
	if req.TTLSeconds > isolateMaxTTLSeconds {
		req.TTLSeconds = isolateMaxTTLSeconds
	}

	username := UsernameFromContext(r.Context())
	// 审计留痕：谁、对哪台主机、隔离还是解除、为什么、多久自动解除
	log.Printf("[响应] 网络隔离 user=%s agent=%s action=%s ttl=%ds reason=%q",
		username, req.AgentID, req.Action, req.TTLSeconds, req.Reason)

	res, err := h.cmdBus.ExecuteSync(req.AgentID, engine.BuildIsolateCmd(isolate, req.Reason, req.TTLSeconds), 30*time.Second)
	if err != nil {
		log.Printf("[响应] 网络隔离下发失败 user=%s agent=%s action=%s err=%v", username, req.AgentID, req.Action, err)
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !res.GetSuccess() {
		log.Printf("[响应] 网络隔离执行失败 user=%s agent=%s action=%s err=%q", username, req.AgentID, req.Action, res.GetErrorMessage())
		writeErr(w, http.StatusInternalServerError, res.GetErrorMessage())
		return
	}
	log.Printf("[响应] 网络隔离完成 user=%s agent=%s action=%s", username, req.AgentID, req.Action)

	// 命令已被 Agent 确认执行成功 → 立即落库隔离状态，Dashboard 无需等 30s 心跳即可看到变化
	// （心跳仍是权威源，会在下一轮校正；这里失败只记日志，不影响命令结果）
	if err := h.store.SetHostIsolated(req.AgentID, isolate); err != nil {
		log.Printf("[响应] 更新主机隔离状态失败 agent=%s isolated=%v err=%v", req.AgentID, isolate, err)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "ok",
		"isolated": isolate,
		"output":   res.GetOutput(),
	})
}
