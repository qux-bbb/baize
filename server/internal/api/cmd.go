// 远程响应操作处理器 — 向在线 Agent 下发指令并同步等待结果
package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
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
