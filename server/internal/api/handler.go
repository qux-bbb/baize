// REST API 处理器 — 查询 Bleve 数据供 Dashboard 调用
package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	pb "github.com/qux-bbb/baize/proto/gen/go/baize/v1"
	"github.com/qux-bbb/baize/server/internal/engine"
	"github.com/qux-bbb/baize/server/internal/store"
)

type Handler struct {
	store   *store.Store
	cmdBus  *engine.CommandBus
	cfg     *engine.ConfigManager
	auth    *AuthManager
	// agentBinary: agent.exe 路径（--agent-binary），zip 打包下载用
	agentBinary string
	// publicAddr: Server 对外 gRPC 地址（--public-addr），注入 agent.conf
	publicAddr string
	// agentInstaller: 预构建的 MSI 安装包路径（--agent-installer），直接下发
	agentInstaller string
	// caFile: TLS CA 证书路径（data-dir/ca.crt），注入下载包（zip 内置 ca.crt，agent.conf ca 字段）
	caFile string
}

func New(s *store.Store, cmdBus *engine.CommandBus, cfg *engine.ConfigManager, auth *AuthManager, agentBinary, publicAddr, agentInstaller, caFile string) *Handler {
	return &Handler{store: s, cmdBus: cmdBus, cfg: cfg, auth: auth, agentBinary: agentBinary, publicAddr: publicAddr, agentInstaller: agentInstaller, caFile: caFile}
}

// ── 登录 ──────────────────────────────────────────────────

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if !h.auth.Verify(req.Username, req.Password) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "用户名或密码错误"})
		return
	}
	mustChange := h.auth.MustChangePassword()
	token := h.auth.IssueToken(req.Username, mustChange)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token":                token,
		"username":             req.Username,
		"must_change_password": mustChange,
	})
}

// ── 修改密码 ──────────────────────────────────────────────

func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if err := h.auth.ChangePassword(req.OldPassword, req.NewPassword); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	username := UsernameFromContext(r.Context())
	token := h.auth.IssueToken(username, false)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token":                token,
		"username":             username,
		"must_change_password": false,
	})
}

// ── 退出登录 ──────────────────────────────────────────────

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	// 从 Authorization 头提取 token 并吊销
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimPrefix(authHeader, "Bearer ")
		h.auth.RevokeToken(token)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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

// Events 事件时间线查询。
// size 取 5000：hostname 已下推 Bleve，q 在 Go 侧模糊过滤，5000 条覆盖足够深的
// 历史窗口（旧逻辑"先取最新100条再过滤"导致搜不到旧数据，已修复）。
const eventsQuerySize = 5000

func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
	hostname := r.URL.Query().Get("hostname")
	q := r.URL.Query().Get("q")
	events, err := h.store.SearchEvents(hostname, q, eventsQuerySize)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if events == nil {
		events = []store.EventResult{}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"events": events, "total": len(events)})
}

// ── 事件类型配置 ──────────────────────────────────────────

// ConfigEventTypes GET: 返回全局 + Agent 级事件类型配置
// POST: 设置事件类型开关（agent_id=空=全局，有值=指定Agent）
func (h *Handler) ConfigEventTypes(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		all := h.cfg.GetEventTypesAll()
		json.NewEncoder(w).Encode(all)
		return
	}

	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}

	var req struct {
		AgentID    string         `json:"agent_id"`
		Categories map[string]bool `json:"categories"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", 400)
		return
	}

	if req.AgentID == "" {
		// 设置全局，广播给所有在线 Agent
		h.cfg.SetEventTypesGlobal(req.Categories)
		effective := h.cfg.GetEventTypesGlobal()
		cmd := engine.BuildConfigureEventTypesCmd(effective)
		h.cmdBus.Broadcast(cmd)
	} else {
		// 设置指定 Agent
		h.cfg.SetEventTypesAgent(req.AgentID, req.Categories)
		effective := h.cfg.GetEffectiveEventTypes(req.AgentID)
		cmd := engine.BuildConfigureEventTypesCmd(effective)
		_ = h.cmdBus.SendToAgent(req.AgentID, cmd)
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ── 导出（CSV） ──────────────────────────────────────────────

// exportLimit 单次导出的最大行数（超出只导出最新的 N 条）
const exportLimit = 50000

// ExportEvents 按当前过滤条件导出事件为 CSV
func (h *Handler) ExportEvents(w http.ResponseWriter, r *http.Request) {
	hostname := r.URL.Query().Get("hostname")
	q := r.URL.Query().Get("q")
	rows, err := h.store.SearchEventsRaw(hostname, q, exportLimit)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	headers := []string{"@timestamp", "hostname", "agent_id", "os_type", "event_type", "event_action",
		"pid", "parent_pid", "process_name", "image_path", "command_line", "user",
		"file_path", "file_size", "hash_sha256",
		"local_ip", "local_port", "remote_ip", "remote_port", "protocol", "direction",
		"registry_key", "registry_value_name", "task_name", "task_path",
		"rule_name", "target_path", "matched_string", "query_name", "query_type", "result_ips"}
	filename := fmt.Sprintf("events_%s.csv", time.Now().Format("20060102_150405"))
	writeCSV(w, filename, headers, rows)
}

// ExportAlerts 导出全部告警为 CSV
func (h *Handler) ExportAlerts(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.SearchAlertsRaw(exportLimit)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	headers := []string{"@timestamp", "alert_id", "rule_id", "rule_name", "severity",
		"hostname", "event_type", "description", "tags", "source_event"}
	filename := fmt.Sprintf("alerts_%s.csv", time.Now().Format("20060102_150405"))
	writeCSV(w, filename, headers, rows)
}

// writeCSV 以 CSV 格式输出（UTF-8 BOM，Excel/WPS 中文不乱码）
func writeCSV(w http.ResponseWriter, filename string, headers []string, rows []map[string]interface{}) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Write([]byte{0xEF, 0xBB, 0xBF})
	cw := csv.NewWriter(w)
	_ = cw.Write(headers)
	for _, row := range rows {
		line := make([]string, len(headers))
		for i, h := range headers {
			line[i] = csvVal(row, h)
		}
		_ = cw.Write(line)
	}
	cw.Flush()
}

// csvVal 将字段值格式化为 CSV 单元格文本（数组字段用 | 分隔）
func csvVal(fields map[string]interface{}, key string) string {
	v, ok := fields[key]
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []interface{}:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok && s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "|")
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

// ── 系统信息查询 ──────────────────────────────────────────

// SystemState 返回某 Agent 的系统状态（首次上线/刷新时采集，Server 覆盖存储的最新一份）
// GET /api/system-state?agent_id=xxx
func (h *Handler) SystemState(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	if agentID == "" {
		http.Error(w, "missing agent_id", 400)
		return
	}
	doc, err := h.store.GetSystemState(agentID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if doc == nil {
		http.Error(w, "该主机暂无系统状态", 404)
		return
	}

	var processes []pb.ProcessInfo
	var tcpConns []pb.ConnectionInfo
	var udpEndpoints []pb.ConnectionInfo
	_ = json.Unmarshal([]byte(doc.Processes), &processes)
	_ = json.Unmarshal([]byte(doc.TCPConnections), &tcpConns)
	_ = json.Unmarshal([]byte(doc.UDPEndpoints), &udpEndpoints)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"agent_id":        doc.AgentID,
		"hostname":        doc.Hostname,
		"captured_at":     doc.CapturedAt,
		"received_at":     doc.ReceivedAt,
		"processes":       processes,
		"tcp_connections": tcpConns,
		"udp_endpoints":   udpEndpoints,
	})
}

func (h *Handler) SystemInfo(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	if agentID == "" {
		http.Error(w, "missing agent_id", 400)
		return
	}
	jsonStr, err := h.cmdBus.QuerySystemInfo(agentID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(jsonStr))
}

// ── CORS ──────────────────────────────────────────────────

func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}
