// Bleve 存储层 — 替代 Elasticsearch，嵌入 Go 二进制
// 零外部依赖，单文件持久化
package store

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/search/query"

	pb "github.com/qux-bbb/baize/proto/gen/go/baize/v1"
)

const (
	eventType = "event"
	alertType = "alert"
)

// Store 封装 Bleve 索引
type Store struct {
	index bleve.Index
}

// EventDoc 事件文档（写入 Bleve 的结构）
type EventDoc struct {
	Type         string `json:"type"`
	Timestamp    string `json:"@timestamp"`
	AgentID      string `json:"agent_id"`
	Hostname     string `json:"hostname"`
	OSType       string `json:"os_type"`
	OSVersion    string `json:"os_version"`
	AgentVersion string `json:"agent_version,omitempty"`
	Arch         string `json:"arch,omitempty"`
	EventType    string `json:"event_type"`
	EventAction  string `json:"event_action,omitempty"`
	Category     string `json:"event_category"`

	// 进程字段
	PID         uint64 `json:"pid,omitempty"`
	ParentPID   uint64 `json:"parent_pid,omitempty"`
	ProcessName string `json:"process_name,omitempty"`
	ImagePath   string `json:"image_path,omitempty"`
	CommandLine string `json:"command_line,omitempty"`
	User        string `json:"user,omitempty"`

	// 文件字段
	FilePath string `json:"file_path,omitempty"`
	FileSize uint64 `json:"file_size,omitempty"`
	HashSHA  string `json:"hash_sha256,omitempty"`

	// 网络字段
	LocalIP    string `json:"local_ip,omitempty"`
	LocalPort  uint32 `json:"local_port,omitempty"`
	RemoteIP   string `json:"remote_ip,omitempty"`
	RemotePort uint32 `json:"remote_port,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
	Direction  string `json:"direction,omitempty"`

	// 注册表
	RegistryKey       string `json:"registry_key,omitempty"`
	RegistryValueName string `json:"registry_value_name,omitempty"`

	// 计划任务
	TaskName string `json:"task_name,omitempty"`
	TaskPath string `json:"task_path,omitempty"`

	// YARA
	RuleName      string `json:"rule_name,omitempty"`
	TargetPath    string `json:"target_path,omitempty"`
	MatchedString string `json:"matched_string,omitempty"`

	// DNS
	QueryName  string `json:"query_name,omitempty"`
	QueryType  string `json:"query_type,omitempty"`
	ResultIPs  string `json:"result_ips,omitempty"`

	// 告警专用
	AlertID      string   `json:"alert_id,omitempty"`
	RuleID       string   `json:"rule_id,omitempty"`
	Severity     string   `json:"severity,omitempty"`
	Description  string   `json:"description,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	SourceEvent  string   `json:"source_event,omitempty"`
}

// New 创建/打开 Bleve 索引
func New(path string) (*Store, error) {
	// 确保目录存在
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("创建目录失败: %w", err)
	}

	var index bleve.Index
	if _, err := os.Stat(path); err == nil {
		// 打开已有索引
		index, err = bleve.Open(path)
		if err != nil {
			return nil, fmt.Errorf("打开索引失败: %w", err)
		}
		log.Printf("[Bleve] 索引 %s 已打开", path)
	} else {
		// 创建新索引
		mapping := bleve.NewIndexMapping()
		docMapping := bleve.NewDocumentMapping()

		// 所有字段按文本索引（支持全文检索）
		textFieldMapping := bleve.NewTextFieldMapping()
		docMapping.AddFieldMappingsAt("hostname", textFieldMapping)
		docMapping.AddFieldMappingsAt("event_type", textFieldMapping)
		docMapping.AddFieldMappingsAt("rule_name", textFieldMapping)
		docMapping.AddFieldMappingsAt("image_path", textFieldMapping)
		docMapping.AddFieldMappingsAt("command_line", textFieldMapping)
		docMapping.AddFieldMappingsAt("file_path", textFieldMapping)
		docMapping.AddFieldMappingsAt("remote_ip", textFieldMapping)
		docMapping.AddFieldMappingsAt("severity", textFieldMapping)

		// 关键词字段（用于聚合/精确匹配）
		keywordMapping := bleve.NewKeywordFieldMapping()
		docMapping.AddFieldMappingsAt("agent_id", keywordMapping)
		docMapping.AddFieldMappingsAt("type", keywordMapping)
		docMapping.AddFieldMappingsAt("alert_id", keywordMapping)

		// 日期字段
		dateMapping := bleve.NewDateTimeFieldMapping()
		docMapping.AddFieldMappingsAt("@timestamp", dateMapping)

		mapping.AddDocumentMapping("doc", docMapping)
		mapping.DefaultMapping = docMapping

		index, err = bleve.New(path, mapping)
		if err != nil {
			return nil, fmt.Errorf("创建索引失败: %w", err)
		}
		log.Printf("[Bleve] 索引 %s 已创建", path)
	}

	return &Store{index: index}, nil
}

// Close 关闭索引
func (s *Store) Close() {
	if s.index != nil {
		s.index.Close()
	}
	log.Println("[Bleve] 索引已关闭")
}

// BleveIndex 返回底层 Bleve 索引（供 API 层使用）
func (s *Store) BleveIndex() bleve.Index {
	return s.index
}

// WriteEvent 索引一条事件
func (s *Store) WriteEvent(event *pb.Event) error {
	doc := eventToDoc(event)
	doc.Type = eventType
	id := fmt.Sprintf("evt-%s-%d", event.GetAgentInfo().GetAgentId(), event.GetSequenceId())
	return s.index.Index(id, doc)
}

// WriteAlert 索引一条告警
func (s *Store) WriteAlert(alert map[string]interface{}) error {
	doc := alertToDoc(alert)
	doc.Type = alertType
	id := fmt.Sprintf("alert-%s", doc.AlertID)
	return s.index.Index(id, doc)
}

// ── 查询方法 ──────────────────────────────────────────────

// SearchHosts 查询所有主机
func (s *Store) SearchHosts(onlineIDs ...[]string) ([]HostResult, error) {
	online := make(map[string]bool)
	if len(onlineIDs) > 0 {
		for _, id := range onlineIDs[0] {
			online[id] = true
		}
	}
	// 查询全部事件，获取所有 agent_id
	q := bleve.NewQueryStringQuery(`type:event`)
	search := bleve.NewSearchRequest(q)
	search.Size = 10000
	search.Fields = []string{"agent_id", "hostname", "os_type", "os_version", "agent_version", "arch", "ip_addresses", "@timestamp"}

	result, err := s.index.Search(search)
	if err != nil {
		return nil, err
	}

	// 按 agent_id 分组
	hostMap := make(map[string]*HostResult)
	hostOrder := []string{}

	for _, hit := range result.Hits {
		agentID := getFieldStr(hit.Fields, "agent_id")

		existing, ok := hostMap[agentID]
		if !ok {
			existing = &HostResult{
				AgentID:  agentID,
				Hostname: getFieldStr(hit.Fields, "hostname"),
				IsOnline: online[agentID],
			}
			hostMap[agentID] = existing
			hostOrder = append(hostOrder, agentID)
		}

		existing.EventCount++
		existing.LastSeen = getFieldStr(hit.Fields, "@timestamp")
		existing.OSType = getFieldStr(hit.Fields, "os_type")
		existing.OSVersion = getFieldStr(hit.Fields, "os_version")
		existing.AgentVersion = getFieldStr(hit.Fields, "agent_version")
		existing.Arch = getFieldStr(hit.Fields, "arch")
	}

	var hosts []HostResult
	for _, id := range hostOrder {
		hosts = append(hosts, *hostMap[id])
	}
	return hosts, nil
}

// SearchAlerts 查询告警
func (s *Store) SearchAlerts(size int) ([]AlertResult, int, error) {
	q := bleve.NewQueryStringQuery(`type:alert`)
	search := bleve.NewSearchRequest(q)
	search.Size = size
	search.SortBy([]string{"-@timestamp"})
	search.Fields = []string{"alert_id", "rule_name", "severity", "hostname", "description", "event_type", "@timestamp", "tags"}

	result, err := s.index.Search(search)
	if err != nil {
		return nil, 0, err
	}

	var alerts []AlertResult
	for _, hit := range result.Hits {
		alerts = append(alerts, AlertResult{
			AlertID:     getFieldStr(hit.Fields, "alert_id"),
			RuleName:    getFieldStr(hit.Fields, "rule_name"),
			Severity:    getFieldStr(hit.Fields, "severity"),
			Hostname:    getFieldStr(hit.Fields, "hostname"),
			Description: getFieldStr(hit.Fields, "description"),
			EventType:   getFieldStr(hit.Fields, "event_type"),
			Timestamp:   getFieldStr(hit.Fields, "@timestamp"),
		})
	}
	return alerts, len(alerts), nil
}

// SearchEvents 查询事件时间线
func (s *Store) SearchEvents(hostname string, size int) ([]EventResult, error) {
	var q query.Query
	if hostname != "" {
		q = bleve.NewQueryStringQuery(fmt.Sprintf(`type:event hostname:"%s"`, hostname))
	} else {
		q = bleve.NewQueryStringQuery(`type:event`)
	}

	search := bleve.NewSearchRequest(q)
	search.Size = size
	search.SortBy([]string{"-@timestamp"})
	search.Fields = []string{"@timestamp", "event_type", "event_action", "pid", "hostname", "image_path", "file_path", "remote_ip", "remote_port", "registry_key", "task_name", "rule_name", "target_path", "command_line", "process_name", "query_name", "query_type", "result_ips"}

	result, err := s.index.Search(search)
	if err != nil {
		return nil, err
	}

	var events []EventResult
	for _, hit := range result.Hits {
		events = append(events, EventResult{
			Timestamp: getFieldStr(hit.Fields, "@timestamp"),
			EventType: getFieldStr(hit.Fields, "event_type"),
			Hostname:  getFieldStr(hit.Fields, "hostname"),
			Summary:   buildSummary(hit.Fields),
			PID:       getFieldUint(hit.Fields, "pid"),
			Raw:       hit.Fields,
		})
	}
	return events, nil
}

// GetAlert 按 alert_id 查询告警详情
func (s *Store) GetAlert(alertID string) (map[string]interface{}, error) {
	q := bleve.NewQueryStringQuery(fmt.Sprintf(`alert_id:"%s"`, alertID))
	search := bleve.NewSearchRequest(q)
	search.Size = 1

	result, err := s.index.Search(search)
	if err != nil {
		return nil, err
	}
	if len(result.Hits) == 0 {
		return nil, fmt.Errorf("告警 %s 不存在", alertID)
	}
	return result.Hits[0].Fields, nil
}

// ── 文档转换 ──────────────────────────────────────────────

func eventToDoc(event *pb.Event) EventDoc {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	doc := EventDoc{
		Timestamp:    now,
		AgentID:      event.GetAgentInfo().GetAgentId(),
		Hostname:     event.GetAgentInfo().GetHostname(),
		OSType:       event.GetAgentInfo().GetOsType(),
		OSVersion:    event.GetAgentInfo().GetOsVersion(),
		AgentVersion: event.GetAgentInfo().GetAgentVersion(),
		Arch:         event.GetAgentInfo().GetArch(),
		EventType:    getEventType(event),
	}

	switch e := event.GetEventType().(type) {
	case *pb.Event_ProcessCreate:
		doc.Category = "process"
		doc.EventAction = "create"
		doc.PID = e.ProcessCreate.GetPid()
		doc.ParentPID = e.ProcessCreate.GetParentPid()
		doc.CommandLine = e.ProcessCreate.GetCommandLine()
		doc.ImagePath = e.ProcessCreate.GetImagePath()
		doc.HashSHA = e.ProcessCreate.GetHashSha256()
		doc.User = e.ProcessCreate.GetUser()
		doc.ProcessName = extractName(e.ProcessCreate.GetImagePath())

	case *pb.Event_ProcessTerminate:
		doc.Category = "process"
		doc.EventAction = "terminate"
		doc.PID = e.ProcessTerminate.GetPid()

	case *pb.Event_FileCreate:
		doc.Category = "file"
		doc.EventAction = "create"
		doc.FilePath = e.FileCreate.GetFilePath()
		doc.FileSize = e.FileCreate.GetFileSize()
		doc.HashSHA = e.FileCreate.GetHashSha256()
		doc.PID = e.FileCreate.GetPid()

	case *pb.Event_FileModify:
		doc.Category = "file"
		doc.EventAction = "modify"
		doc.FilePath = e.FileModify.GetFilePath()

	case *pb.Event_FileDelete:
		doc.Category = "file"
		doc.EventAction = "delete"
		doc.FilePath = e.FileDelete.GetFilePath()
		doc.FileSize = e.FileDelete.GetOriginalSize()

	case *pb.Event_NetworkConnection:
		doc.Category = "network"
		doc.LocalIP = e.NetworkConnection.GetLocalIp()
		doc.LocalPort = e.NetworkConnection.GetLocalPort()
		doc.RemoteIP = e.NetworkConnection.GetRemoteIp()
		doc.RemotePort = e.NetworkConnection.GetRemotePort()
		doc.Protocol = e.NetworkConnection.GetProtocol()
		doc.Direction = e.NetworkConnection.GetDirection()
		doc.PID = e.NetworkConnection.GetPid()
		doc.ProcessName = extractName(e.NetworkConnection.GetProcessName())

	case *pb.Event_RegistryChange:
		doc.Category = "registry"
		doc.RegistryKey = e.RegistryChange.GetKeyPath()
		doc.RegistryValueName = e.RegistryChange.GetValueName()

	case *pb.Event_ScheduledTask:
		doc.Category = "scheduled_task"
		doc.TaskName = e.ScheduledTask.GetTaskName()
		doc.TaskPath = e.ScheduledTask.GetTaskPath()

	case *pb.Event_YaraMatch:
		doc.Category = "yara"
		doc.RuleName = e.YaraMatch.GetRuleName()
		doc.TargetPath = e.YaraMatch.GetTargetPath()
		doc.MatchedString = e.YaraMatch.GetMatchedString()

	case *pb.Event_DnsQuery:
		doc.Category = "network"
		doc.EventAction = "query"
		doc.PID = e.DnsQuery.GetPid()
		doc.ProcessName = extractName(e.DnsQuery.GetProcessName())
		doc.QueryName = e.DnsQuery.GetQueryName()
		doc.QueryType = e.DnsQuery.GetQueryType()
		doc.ResultIPs = e.DnsQuery.GetResultIps()
	}

	return doc
}

func alertToDoc(alert map[string]interface{}) EventDoc {
	doc := EventDoc{
		Timestamp:   getMapStr(alert, "@timestamp"),
		AlertID:     getMapStr(alert, "alert_id"),
		RuleName:    getMapStr(alert, "rule_name"),
		RuleID:      getMapStr(alert, "rule_id"),
		Severity:    getMapStr(alert, "severity"),
		Hostname:    getMapStr(alert, "hostname"),
		Description: getMapStr(alert, "description"),
		EventType:   getMapStr(alert, "event_type"),
		Category:    "alert",
		SourceEvent: getMapStr(alert, "source_event"),
	}
	if tags, ok := alert["tags"].([]string); ok {
		doc.Tags = tags
	}
	return doc
}

func getEventType(event *pb.Event) string {
	switch event.GetEventType().(type) {
	case *pb.Event_ProcessCreate:
		return "process_create"
	case *pb.Event_ProcessTerminate:
		return "process_terminate"
	case *pb.Event_FileCreate:
		return "file_create"
	case *pb.Event_FileModify:
		return "file_modify"
	case *pb.Event_FileDelete:
		return "file_delete"
	case *pb.Event_NetworkConnection:
		return "network_connection"
	case *pb.Event_RegistryChange:
		return "registry_change"
	case *pb.Event_ScheduledTask:
		return "scheduled_task"
	case *pb.Event_YaraMatch:
		return "yara_match"
	case *pb.Event_DnsQuery:
		return "dns_query"
	}
	return "unknown"
}

func extractName(path string) string {
	if path == "" {
		return ""
	}
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '\\' || path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

func getMapStr(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type HostResult struct {
	AgentID    string `json:"agent_id"`
	Hostname   string `json:"hostname"`
	EventCount int    `json:"event_count"`
	LastSeen   string `json:"last_seen"`
	IsOnline   bool   `json:"is_online"`
	OSType      string `json:"os_type"`
	OSVersion   string `json:"os_version"`
	AgentVersion string `json:"agent_version"`
	Arch        string `json:"arch"`
}

type AlertResult struct {
	AlertID     string `json:"alert_id"`
	RuleName    string `json:"rule_name"`
	Severity    string `json:"severity"`
	Hostname    string `json:"hostname"`
	Description string `json:"description"`
	EventType   string `json:"event_type"`
	Timestamp   string `json:"@timestamp"`
}

type EventResult struct {
	Timestamp string                 `json:"@timestamp"`
	EventType string                 `json:"event_type"`
	Hostname  string                 `json:"hostname"`
	Summary   string                 `json:"summary"`
	PID       uint64                 `json:"pid,omitempty"`
	Raw       map[string]interface{} `json:"raw,omitempty"`
}

func getFieldStr(fields map[string]interface{}, key string) string {
	if v, ok := fields[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getFieldUint(fields map[string]interface{}, key string) uint64 {
	if v, ok := fields[key]; ok {
		switch n := v.(type) {
		case float64:
			return uint64(n)
		case uint64:
			return n
		}
	}
	return 0
}

func buildSummary(fields map[string]interface{}) string {
	eventType := getFieldStr(fields, "event_type")

	// 进程事件：用 path，没有则用 pid
	if eventType == "process_create" || eventType == "process_terminate" {
		if img := getFieldStr(fields, "image_path"); img != "" {
			return img
		}
		if pid := getFieldUint(fields, "pid"); pid > 0 {
			return fmt.Sprintf("[PID %d] %s", pid, eventType)
		}
		return eventType
	}

	if img := getFieldStr(fields, "image_path"); img != "" {
		return img
	}
	if fp := getFieldStr(fields, "file_path"); fp != "" {
		return fp
	}
	if rip := getFieldStr(fields, "remote_ip"); rip != "" {
		port := getFieldUint(fields, "remote_port")
		proc := getFieldStr(fields, "process_name")
		pid := getFieldUint(fields, "pid")
		if proc != "" && pid > 0 {
			return fmt.Sprintf("%s (PID %d) → %s:%d", proc, pid, rip, port)
		} else if proc != "" {
			return fmt.Sprintf("%s → %s:%d", proc, rip, port)
		}
		return fmt.Sprintf("%s:%d", rip, port)
	}
	if rk := getFieldStr(fields, "registry_key"); rk != "" {
		return rk
	}
	if tn := getFieldStr(fields, "task_name"); tn != "" {
		return tn
	}
	if rn := getFieldStr(fields, "rule_name"); rn != "" {
		return fmt.Sprintf("%s → %s", rn, getFieldStr(fields, "target_path"))
	}
	if qn := getFieldStr(fields, "query_name"); qn != "" {
		proc := getFieldStr(fields, "process_name")
		rip := getFieldStr(fields, "result_ips")
		if proc != "" && rip != "" {
			return fmt.Sprintf("%s → %s (%s)", proc, qn, rip)
		} else if proc != "" {
			return fmt.Sprintf("%s → %s", proc, qn)
		}
		return qn
	}
	if cl := getFieldStr(fields, "command_line"); cl != "" {
		if len(cl) > 120 {
			cl = cl[:120]
		}
		return cl
	}
	return eventType
}
