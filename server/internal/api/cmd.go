// 远程响应操作处理器 — 向在线 Agent 下发指令并同步等待结果
package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/qux-bbb/baize/server/internal/engine"
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
