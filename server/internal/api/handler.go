// REST API 处理器 — 查询 Bleve 数据供 Dashboard 调用
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	pb "github.com/qux-bbb/baize/proto/gen/go/baize/v1"
	"github.com/qux-bbb/baize/server/internal/engine"
	"github.com/qux-bbb/baize/server/internal/store"
)

type Handler struct {
	store   *store.Store
	cmdBus  *engine.CommandBus
	cfg     *engine.ConfigManager
}

func New(s *store.Store, cmdBus *engine.CommandBus, cfg *engine.ConfigManager) *Handler {
	return &Handler{store: s, cmdBus: cmdBus, cfg: cfg}
}

// ── 主机列表 ──────────────────────────────────────────────

func (h *Handler) Hosts(w http.ResponseWriter, r *http.Request) {
	onlineIDs := h.cmdBus.AgentIDs()
	hosts, err := h.store.SearchHosts(onlineIDs)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if hosts == nil {
		hosts = []store.HostResult{}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"hosts": hosts})
}

// ── 告警列表 ──────────────────────────────────────────────

func (h *Handler) Alerts(w http.ResponseWriter, r *http.Request) {
	alerts, total, err := h.store.SearchAlerts(50)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if alerts == nil {
		alerts = []store.AlertResult{}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"alerts": alerts, "total": total})
}

// ── 告警详情 ──────────────────────────────────────────────

func (h *Handler) AlertDetail(w http.ResponseWriter, r *http.Request) {
	alertID := r.URL.Query().Get("alert_id")
	if alertID == "" {
		http.Error(w, "missing alert_id", 400)
		return
	}

	doc, err := h.store.GetAlert(alertID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	json.NewEncoder(w).Encode(doc)
}

// ── 文件监控配置 ─────────────────────────────────────────

func (h *Handler) ConfigFileWatch(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		configs := h.cfg.GetAll()
		json.NewEncoder(w).Encode(configs)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		AgentID string   `json:"agent_id"`
		Dirs    []string `json:"dirs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", 400)
		return
	}
	if req.AgentID == "" {
		h.cfg.SetGlobal(req.Dirs)
	} else {
		h.cfg.SetAgent(req.AgentID, req.Dirs)
	}

	// 立即推送给在线 Agent
	cmd := &pb.Command{
		CommandId:  fmt.Sprintf("filewatch-%d", time.Now().UnixNano()),
		IssuedAtNs: uint64(time.Now().UnixNano()),
		CommandType: &pb.Command_ConfigureFileWatch{
			ConfigureFileWatch: &pb.ConfigureFileWatchCommand{
				WatchDirs: req.Dirs,
				Reason:    "api config",
			},
		},
	}
	if req.AgentID == "" {
		h.cmdBus.Broadcast(cmd)
	} else {
		h.cmdBus.SendToAgent(req.AgentID, cmd)
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ── 事件时间线 ──────────────────────────────────────────────

func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
	hostname := r.URL.Query().Get("hostname")
	events, err := h.store.SearchEvents(hostname, 100)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if events == nil {
		events = []store.EventResult{}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"events": events, "total": len(events)})
}

// ── CORS ──────────────────────────────────────────────────

func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}
