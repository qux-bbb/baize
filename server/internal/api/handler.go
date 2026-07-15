// REST API 处理器 — 查询 ES 数据供 Dashboard 调用
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
	"strings"
)

const (
	eventsIndex = "baize-events-*"
	alertsIndex = "baize-alerts-*"
)

type Handler struct {
	es *elasticsearch.Client
}

func New(es *elasticsearch.Client) *Handler {
	return &Handler{es: es}
}

// ── 主机列表 ──────────────────────────────────────────────

type HostSummary struct {
	AgentID      string   `json:"agent_id"`
	Hostname     string   `json:"hostname"`
	OSType       string   `json:"os_type"`
	OSVersion    string   `json:"os_version"`
	AgentVersion string   `json:"agent_version,omitempty"`
	Arch         string   `json:"arch,omitempty"`
	EventCount   int      `json:"event_count"`
	LastSeen     string   `json:"last_seen"`
	IPs          []string `json:"ips,omitempty"`
}

func (h *Handler) Hosts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 聚合: 按 agent_id + hostname 分组统计
	query := `{
		"size": 0,
		"aggs": {
			"hosts": {
				"terms": { "field": "agent_id.keyword", "size": 100, "missing": "unknown" },
				"aggs": {
					"latest": { "top_hits": { "size": 1, "sort": [{"@timestamp": "desc"}] } }
				}
			}
		}
	}`

	req := esapi.SearchRequest{
		Index: []string{eventsIndex},
		Body:  strings.NewReader(query),
	}
	res, err := req.Do(ctx, h.es)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer res.Body.Close()

	var raw map[string]any
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	var hosts []HostSummary
	aggs, _ := raw["aggregations"].(map[string]any)
	if aggs != nil {
		hostsBucket, _ := aggs["hosts"].(map[string]any)
		if hostsBucket != nil {
			buckets, _ := hostsBucket["buckets"].([]any)
			for _, b := range buckets {
				bkt := b.(map[string]any)
				top, _ := bkt["latest"].(map[string]any)["hits"].(map[string]any)["hits"].([]any)
				if len(top) == 0 {
					continue
				}
				src, _ := top[0].(map[string]any)["_source"].(map[string]any)
				hosts = append(hosts, HostSummary{
					AgentID:      getStr(src, "agent_id"),
					Hostname:     getStr(src, "hostname"),
					OSType:       getStr(src, "os_type"),
					OSVersion:    getStr(src, "os_version"),
					AgentVersion: getStr(src, "agent_version"),
					Arch:         getStr(src, "arch"),
					EventCount:   int(getFloat(bkt, "doc_count")),
					LastSeen:     getStr(src, "@timestamp"),
					IPs:          getStrs(src, "ip_addresses"),
				})
			}
		}
	}
	if hosts == nil {
		hosts = []HostSummary{}
	}
	json.NewEncoder(w).Encode(map[string]any{"hosts": hosts})
}

// ── 告警列表 ──────────────────────────────────────────────

type AlertItem struct {
	AlertID     string   `json:"alert_id"`
	RuleName    string   `json:"rule_name"`
	Severity    string   `json:"severity"`
	Hostname    string   `json:"hostname"`
	Description string   `json:"description"`
	EventType   string   `json:"event_type"`
	Timestamp   string   `json:"@timestamp"`
	Tags        []string `json:"tags,omitempty"`
}

func (h *Handler) Alerts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	size := 50
	query := fmt.Sprintf(`{
		"size": %d,
		"sort": [{"@timestamp": "desc"}]
	}`, size)

	req := esapi.SearchRequest{
		Index: []string{alertsIndex},
		Body:  strings.NewReader(query),
	}
	res, err := req.Do(ctx, h.es)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer res.Body.Close()

	var raw map[string]any
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	var alerts []AlertItem
	hits, _ := raw["hits"].(map[string]any)["hits"].([]any)
	for _, hit := range hits {
		src := hit.(map[string]any)["_source"].(map[string]any)
		alerts = append(alerts, AlertItem{
			AlertID:     getStr(src, "alert_id"),
			RuleName:    getStr(src, "rule_name"),
			Severity:    getStr(src, "severity"),
			Hostname:    getStr(src, "hostname"),
			Description: getStr(src, "description"),
			EventType:   getStr(src, "event_type"),
			Timestamp:   getStr(src, "@timestamp"),
			Tags:        getStrs(src, "tags"),
		})
	}
	if alerts == nil {
		alerts = []AlertItem{}
	}
	json.NewEncoder(w).Encode(map[string]any{"alerts": alerts, "total": len(alerts)})
}

// ── 事件时间线 ──────────────────────────────────────────────

type EventItem struct {
	Timestamp   string `json:"@timestamp"`
	EventType   string `json:"event_type"`
	EventAction string `json:"event_action,omitempty"`
	Summary     string `json:"summary"`
	PID         uint64 `json:"pid,omitempty"`
	Hostname    string `json:"hostname"`
}

func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	hostname := r.URL.Query().Get("hostname")
	size := 100

	var query string
	if hostname != "" {
		query = fmt.Sprintf(`{
			"size": %d,
			"sort": [{"@timestamp": "desc"}],
			"query": {"term": {"hostname": "%s"}}
		}`, size, hostname)
	} else {
		query = fmt.Sprintf(`{
			"size": %d,
			"sort": [{"@timestamp": "desc"}]
		}`, size)
	}

	req := esapi.SearchRequest{
		Index: []string{eventsIndex},
		Body:  strings.NewReader(query),
	}
	res, err := req.Do(ctx, h.es)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer res.Body.Close()

	var raw map[string]any
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	var events []EventItem
	hits, _ := raw["hits"].(map[string]any)["hits"].([]any)
	for _, hit := range hits {
		src := hit.(map[string]any)["_source"].(map[string]any)
		events = append(events, EventItem{
			Timestamp:   getStr(src, "@timestamp"),
			EventType:   getStr(src, "event_type"),
			EventAction: getStr(src, "event_action"),
			Summary:     buildSummary(src),
			PID:         uint64(getFloat(src, "pid")),
			Hostname:    getStr(src, "hostname"),
		})
	}
	if events == nil {
		events = []EventItem{}
	}
	json.NewEncoder(w).Encode(map[string]any{"events": events, "total": len(events)})
}

// ── 告警详情（含原始事件）────────────────────────────────

type AlertDetail struct {
	AlertID     string                 `json:"alert_id"`
	RuleName    string                 `json:"rule_name"`
	RuleID      string                 `json:"rule_id"`
	Severity    string                 `json:"severity"`
	Hostname    string                 `json:"hostname"`
	Description string                 `json:"description"`
	Timestamp   string                 `json:"@timestamp"`
	Tags        []string               `json:"tags,omitempty"`
	SourceEvent map[string]interface{} `json:"source_event,omitempty"`
	EventType   string                 `json:"event_type"`
}

func (h *Handler) AlertDetail(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	alertID := r.URL.Query().Get("alert_id")
	if alertID == "" {
		http.Error(w, "missing alert_id", 400)
		return
	}

	query := fmt.Sprintf(`{
		"size": 1,
		"query": {"term": {"alert_id": "%s"}}
	}`, alertID)

	req := esapi.SearchRequest{
		Index: []string{alertsIndex},
		Body:  strings.NewReader(query),
	}
	res, err := req.Do(ctx, h.es)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer res.Body.Close()

	var raw map[string]any
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	hits, _ := raw["hits"].(map[string]any)["hits"].([]any)
	if len(hits) == 0 {
		http.Error(w, "not found", 404)
		return
	}

	src := hits[0].(map[string]any)["_source"].(map[string]any)
	detail := AlertDetail{
		AlertID:     getStr(src, "alert_id"),
		RuleName:    getStr(src, "rule_name"),
		RuleID:      getStr(src, "rule_id"),
		Severity:    getStr(src, "severity"),
		Hostname:    getStr(src, "hostname"),
		Description: getStr(src, "description"),
		Timestamp:   getStr(src, "@timestamp"),
		Tags:        getStrs(src, "tags"),
		EventType:   getStr(src, "event_type"),
	}

	// 解析 source_event 字符串为 JSON 对象
	if seStr := getStr(src, "source_event"); seStr != "" {
		var se map[string]interface{}
		if err := json.Unmarshal([]byte(seStr), &se); err == nil {
			detail.SourceEvent = se
		}
	}

	json.NewEncoder(w).Encode(detail)
}

// ── 辅助 ──────────────────────────────────────────────────

func getStr(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func getStrs(m map[string]any, key string) []string {
	v, _ := m[key].([]any)
	if len(v) == 0 {
		return nil
	}
	res := make([]string, len(v))
	for i, item := range v {
		res[i], _ = item.(string)
	}
	return res
}

func getFloat(m map[string]any, key string) float64 {
	v, _ := m[key].(float64)
	return v
}

func buildSummary(src map[string]any) string {
	et := getStr(src, "event_type")
	switch {
	case strings.HasPrefix(et, "process"):
		if img := getStr(src, "image_path"); img != "" {
			return img
		}
		return fmt.Sprintf("PID %d", uint64(getFloat(src, "pid")))
	case strings.HasPrefix(et, "file"):
		return getStr(src, "file_path")
	case strings.HasPrefix(et, "network"):
		return fmt.Sprintf("%s:%s → %s:%s",
			getStr(src, "local_ip"), getStr(src, "local_port"),
			getStr(src, "remote_ip"), getStr(src, "remote_port"))
	case strings.HasPrefix(et, "registry"):
		return getStr(src, "registry_key")
	case strings.HasPrefix(et, "scheduled_task"):
		return getStr(src, "task_name")
	case strings.HasPrefix(et, "yara"):
		return fmt.Sprintf("%s → %s", getStr(src, "rule_name"), getStr(src, "target_path"))
	}
	return et
}

// ── CORS 中间件（Dashboard 开发期跨域）─────────────────────

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
