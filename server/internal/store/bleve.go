// Bleve 存储层 — 替代 Elasticsearch，嵌入 Go 二进制
// 零外部依赖，单文件持久化
package store

import (
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blevesearch/bleve/v2"
	bquery "github.com/blevesearch/bleve/v2/search/query"
	index "github.com/blevesearch/bleve_index_api"

	pb "github.com/qux-bbb/baize/proto/gen/go/baize/v1"
)

const (
	eventType = "event"
	alertType = "alert"
	hostType  = "host"
)

// hostUpdateInterval 事件流更新 host 文档的节流间隔（避免事件密集时写放大）
const hostUpdateInterval = 10 * time.Second

// Store 封装 Bleve 索引
type Store struct {
	index bleve.Index

	// 主机实体状态（内存态，重启后由 RebuildHosts 填充）
	hostMu       sync.Mutex
	hostStates   map[string]*hostState
	hostThrottle map[string]time.Time
}

// hostState 内存中的主机状态（写文档时落盘）
type hostState struct {
	firstSeen string
	lastSeen  string
	count     uint64 // 已见过的事件最大 seq（= 事件数）
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

	return &Store{
		index:        index,
		hostStates:   make(map[string]*hostState),
		hostThrottle: make(map[string]time.Time),
	}, nil
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

// ── 主机实体 ──────────────────────────────────────────────
// 主机是独立实体（type:host 文档），由 心跳 RPC / 事件流 两条路径维护，
// 主机列表直接查询主机文档，不再从事件聚合。

// HostDoc 主机文档（写入 Bleve 的结构，docID = host-<agent_id>，同 id 覆盖 = upsert）
type HostDoc struct {
	Type         string   `json:"type"`
	AgentID      string   `json:"agent_id"`
	Hostname     string   `json:"hostname"`
	OSType       string   `json:"os_type"`
	OSVersion    string   `json:"os_version"`
	AgentVersion string   `json:"agent_version"`
	Arch         string   `json:"arch"`
	IPAddresses  []string `json:"ip_addresses"`
	FirstSeen    string   `json:"first_seen"`
	LastSeen     string   `json:"last_seen"`
	EventCount   uint64   `json:"event_count"`
}

// WriteHost 注册/心跳：写入（upsert）主机文档
// 调用方：Heartbeat RPC（低频，30s/次），不走节流
func (s *Store) WriteHost(info *pb.AgentInfo, lastSeen string) error {
	if info == nil || info.GetAgentId() == "" {
		return nil
	}
	agentID := info.GetAgentId()

	s.hostMu.Lock()
	defer s.hostMu.Unlock()

	st := s.hostStates[agentID]
	if st == nil {
		st = &hostState{firstSeen: lastSeen}
		s.hostStates[agentID] = st
	}
	st.lastSeen = lastSeen
	s.hostThrottle[agentID] = time.Now()

	doc := &HostDoc{
		Type:         hostType,
		AgentID:      agentID,
		Hostname:     info.GetHostname(),
		OSType:       info.GetOsType(),
		OSVersion:    info.GetOsVersion(),
		AgentVersion: info.GetAgentVersion(),
		Arch:         info.GetArch(),
		IPAddresses:  info.GetIpAddresses(),
		FirstSeen:    st.firstSeen,
		LastSeen:     st.lastSeen,
		EventCount:   st.count,
	}
	return s.index.Index("host-"+agentID, doc)
}

// UpsertHostFromEvent 事件写入时的伴随更新（节流 10s，避免写放大）
// 首次见到该 agent 时立即注册，之后按 hostUpdateInterval 节流合并
func (s *Store) UpsertHostFromEvent(event *pb.Event) error {
	info := event.GetAgentInfo()
	if info == nil || info.GetAgentId() == "" {
		return nil
	}
	agentID := info.GetAgentId()
	seq := event.GetSequenceId()
	ts := EventTime(event)

	s.hostMu.Lock()
	defer s.hostMu.Unlock()

	st := s.hostStates[agentID]
	if st == nil {
		// 首次：尝试从索引恢复状态（重启后 RebuildHosts 完成前的窗口）
		st = s.getHostStateFromIndex(agentID)
		if st == nil {
			st = &hostState{firstSeen: ts}
		}
		s.hostStates[agentID] = st
	}
	if seq > st.count {
		st.count = seq
	}
	st.lastSeen = ts

	// 节流：间隔内只更新内存，不写索引
	if last, ok := s.hostThrottle[agentID]; ok && time.Since(last) < hostUpdateInterval {
		return nil
	}

	s.hostThrottle[agentID] = time.Now()
	doc := &HostDoc{
		Type:         hostType,
		AgentID:      agentID,
		Hostname:     info.GetHostname(),
		OSType:       info.GetOsType(),
		OSVersion:    info.GetOsVersion(),
		AgentVersion: info.GetAgentVersion(),
		Arch:         info.GetArch(),
		IPAddresses:  info.GetIpAddresses(),
		FirstSeen:    st.firstSeen,
		LastSeen:     st.lastSeen,
		EventCount:   st.count,
	}
	return s.index.Index("host-"+agentID, doc)
}

// getHostStateFromIndex 从索引读取主机文档状态（仅用于重启后的恢复窗口）
func (s *Store) getHostStateFromIndex(agentID string) *hostState {
	doc, err := s.index.Document("host-" + agentID)
	if err != nil || doc == nil {
		return nil
	}
	st := &hostState{}
	doc.VisitFields(func(f index.Field) {
		switch f.Name() {
		case "first_seen":
			st.firstSeen = string(f.Value())
		case "last_seen":
			st.lastSeen = string(f.Value())
		case "event_count":
			// bleve 数字字段存储为 8 字节大端 float64
			if v := f.Value(); len(v) == 8 {
				bits := binary.BigEndian.Uint64(v)
				st.count = uint64(math.Float64frombits(bits))
			} else if n, err := strconv.ParseUint(strings.TrimSpace(string(v)), 10, 64); err == nil {
				st.count = n
			}
		}
	})
	return st
}

// RebuildHosts 从事件索引重建所有主机文档（Server 启动时后台执行一次，幂等）
// 用 facet 按 agent_id 分组拿精确事件数，再对每个 agent 查最新事件取静态字段
func (s *Store) RebuildHosts() (int, error) {
	// 1. facet 分组拿精确事件数（无 size 截断问题）
	q := bleve.NewQueryStringQuery("type:event")
	search := bleve.NewSearchRequest(q)
	search.Size = 0
	facet := bleve.NewFacetRequest("agent_id", 10000)
	search.AddFacet("hosts", facet)

	result, err := s.index.Search(search)
	if err != nil {
		return 0, err
	}
	f, ok := result.Facets["hosts"]
	if !ok {
		return 0, nil
	}

	terms := f.Terms.Terms()
	for _, t := range terms {
		agentID := t.Term
		// 2. 查该 agent 的最新一条事件（静态字段 + last_seen）
		q2 := bleve.NewQueryStringQuery(fmt.Sprintf(`type:event AND agent_id:"%s"`, agentID))
		s2 := bleve.NewSearchRequest(q2)
		s2.Size = 1
		s2.SortBy([]string{"-@timestamp"})
		s2.Fields = []string{"agent_id", "hostname", "os_type", "os_version", "agent_version", "arch", "@timestamp"}
		r2, err := s.index.Search(s2)
		if err != nil || len(r2.Hits) == 0 {
			continue
		}
		hit := r2.Hits[0]
		lastSeen := getFieldStr(hit.Fields, "@timestamp")

		s.hostMu.Lock()
		st := s.hostStates[agentID]
		if st == nil {
			st = &hostState{firstSeen: lastSeen}
			s.hostStates[agentID] = st
		}
		st.lastSeen = lastSeen
		if uint64(t.Count) > st.count {
			st.count = uint64(t.Count)
		}
		s.hostThrottle[agentID] = time.Now()
		doc := &HostDoc{
			Type:         hostType,
			AgentID:      agentID,
			Hostname:     getFieldStr(hit.Fields, "hostname"),
			OSType:       getFieldStr(hit.Fields, "os_type"),
			OSVersion:    getFieldStr(hit.Fields, "os_version"),
			AgentVersion: getFieldStr(hit.Fields, "agent_version"),
			Arch:         getFieldStr(hit.Fields, "arch"),
			FirstSeen:    st.firstSeen,
			LastSeen:     st.lastSeen,
			EventCount:   st.count,
		}
		err = s.index.Index("host-"+agentID, doc)
		s.hostMu.Unlock()
		if err != nil {
			return 0, err
		}
	}
	return len(terms), nil
}

// ── 查询方法 ──────────────────────────────────────────────

// SearchHosts 查询所有主机（查独立的主机文档 type:host，毫秒级）
func (s *Store) SearchHosts(onlineIDs ...[]string) ([]HostResult, error) {
	online := make(map[string]bool)
	if len(onlineIDs) > 0 {
		for _, id := range onlineIDs[0] {
			online[id] = true
		}
	}

	q := bleve.NewQueryStringQuery(`type:host`)
	search := bleve.NewSearchRequest(q)
	search.Size = 10000
	search.Fields = []string{"agent_id", "hostname", "os_type", "os_version", "agent_version", "arch", "ip_addresses", "first_seen", "last_seen", "event_count"}

	result, err := s.index.Search(search)
	if err != nil {
		return nil, err
	}

	hosts := make([]HostResult, 0, len(result.Hits))
	for _, hit := range result.Hits {
		agentID := getFieldStr(hit.Fields, "agent_id")
		if agentID == "" {
			continue
		}
		hosts = append(hosts, HostResult{
			AgentID:      agentID,
			Hostname:     getFieldStr(hit.Fields, "hostname"),
			IsOnline:     online[agentID],
			EventCount:   int(getFieldUint(hit.Fields, "event_count")),
			LastSeen:     getFieldStr(hit.Fields, "last_seen"),
			OSType:       getFieldStr(hit.Fields, "os_type"),
			OSVersion:    getFieldStr(hit.Fields, "os_version"),
			AgentVersion: getFieldStr(hit.Fields, "agent_version"),
			Arch:         getFieldStr(hit.Fields, "arch"),
			Ips:          getFieldStrs(hit.Fields, "ip_addresses"),
		})
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

// SearchAlertsRaw 查询告警原始字段（CSV 导出用，字段全量）
func (s *Store) SearchAlertsRaw(size int) ([]map[string]interface{}, error) {
	q := bleve.NewQueryStringQuery(`type:alert`)
	search := bleve.NewSearchRequest(q)
	search.Size = size
	search.SortBy([]string{"-@timestamp"})
	search.Fields = []string{"alert_id", "rule_id", "rule_name", "severity", "hostname", "description", "event_type", "@timestamp", "tags", "source_event"}

	result, err := s.index.Search(search)
	if err != nil {
		return nil, err
	}

	rows := make([]map[string]interface{}, 0, len(result.Hits))
	for _, hit := range result.Hits {
		rows = append(rows, hit.Fields)
	}
	return rows, nil
}

// SearchEventsRaw 查询事件原始字段（CSV 导出用，字段全量，过滤逻辑与 SearchEvents 一致）
func (s *Store) SearchEventsRaw(hostname, query string, size int) ([]map[string]interface{}, error) {
	// hostname 下推到 Bleve 查询（精确 phrase 匹配）；query 仍 Go 侧模糊过滤
	// （q 语义杂：PID/IP/域名/文件名，Bleve 分词对 192.168.1.1、pid=123 不可控，Contains 最符合预期）
	var q bquery.Query = bleve.NewQueryStringQuery("type:event")
	if hostname != "" {
		mp := bleve.NewMatchPhraseQuery(hostname)
		mp.SetField("hostname")
		q = bleve.NewConjunctionQuery(q, mp)
	}

	search := bleve.NewSearchRequest(q)
	search.Size = size
	search.SortBy([]string{"-@timestamp"})
	search.Fields = []string{"@timestamp", "event_type", "event_action", "pid", "hostname", "agent_id", "os_type", "parent_pid", "process_name", "image_path", "command_line", "user", "file_path", "file_size", "hash_sha256", "local_ip", "local_port", "remote_ip", "remote_port", "protocol", "direction", "registry_key", "registry_value_name", "task_name", "task_path", "rule_name", "target_path", "matched_string", "query_name", "query_type", "result_ips"}
	result, err := s.index.Search(search)
	if err != nil {
		return nil, err
	}

	var rows []map[string]interface{}
	for _, hit := range result.Hits {
		// query Go 侧过滤（hostname 已下推）
		if query != "" {
			matched := false
			for _, f := range []string{"hostname", "event_type", "image_path", "command_line",
				"file_path", "local_ip", "remote_ip", "process_name", "query_name", "result_ips",
				"summary", "registry_key", "task_name", "target_path", "protocol", "direction"} {
				if v := getFieldStr(hit.Fields, f); v != "" && strings.Contains(strings.ToLower(v), strings.ToLower(query)) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		rows = append(rows, hit.Fields)
	}
	return rows, nil
}

// SearchEvents 查询事件时间线
func (s *Store) SearchEvents(hostname, query string, size int) ([]EventResult, error) {
	// hostname 下推到 Bleve 查询（精确 phrase 匹配）；query 仍 Go 侧模糊过滤
	// （q 语义杂：PID/IP/域名/文件名，Bleve 分词对 192.168.1.1、pid=123 不可控，Contains 最符合预期）
	var q bquery.Query = bleve.NewQueryStringQuery("type:event")
	if hostname != "" {
		mp := bleve.NewMatchPhraseQuery(hostname)
		mp.SetField("hostname")
		q = bleve.NewConjunctionQuery(q, mp)
	}

	search := bleve.NewSearchRequest(q)
	search.Size = size
	search.SortBy([]string{"-@timestamp"})
	search.Fields = []string{"@timestamp", "event_type", "event_action", "pid", "hostname", "image_path", "file_path", "local_ip", "local_port", "remote_ip", "remote_port", "registry_key", "task_name", "rule_name", "target_path", "command_line", "process_name", "query_name", "query_type", "result_ips"}

	result, err := s.index.Search(search)
	if err != nil {
		return nil, err
	}

	var events []EventResult
	for _, hit := range result.Hits {
		// query Go 侧过滤（hostname 已下推）
		if query != "" {
			// 检查所有字段
			matched := false
			for _, f := range []string{"hostname","event_type","image_path","command_line",
				"file_path","local_ip","remote_ip","process_name","query_name","result_ips",
				"summary","registry_key","task_name","target_path","protocol","direction"} {
				if v := getFieldStr(hit.Fields, f); v != "" && strings.Contains(strings.ToLower(v), strings.ToLower(query)) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
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

// EventTime 返回事件发生时间 (RFC3339Nano, UTC)。
// 直接取 Agent 上报的 timestamp_ns，不回退服务端时间。
func EventTime(event *pb.Event) string {
	var ts uint64
	switch e := event.GetEventType().(type) {
	case *pb.Event_ProcessCreate:
		ts = e.ProcessCreate.GetTimestampNs()
	case *pb.Event_ProcessTerminate:
		ts = e.ProcessTerminate.GetTimestampNs()
	case *pb.Event_FileCreate:
		ts = e.FileCreate.GetTimestampNs()
	case *pb.Event_FileModify:
		ts = e.FileModify.GetTimestampNs()
	case *pb.Event_FileDelete:
		ts = e.FileDelete.GetTimestampNs()
	case *pb.Event_NetworkConnection:
		ts = e.NetworkConnection.GetTimestampNs()
	case *pb.Event_RegistryChange:
		ts = e.RegistryChange.GetTimestampNs()
	case *pb.Event_ScheduledTask:
		ts = e.ScheduledTask.GetTimestampNs()
	case *pb.Event_YaraMatch:
		ts = e.YaraMatch.GetTimestampNs()
	case *pb.Event_DnsQuery:
		ts = e.DnsQuery.GetTimestampNs()
	}
	return time.Unix(0, int64(ts)).UTC().Format(time.RFC3339Nano)
}

func eventToDoc(event *pb.Event) EventDoc {
	now := EventTime(event)
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
		doc.ImagePath = e.NetworkConnection.GetImagePath()

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
	AgentID      string   `json:"agent_id"`
	Hostname     string   `json:"hostname"`
	EventCount   int      `json:"event_count"`
	LastSeen     string   `json:"last_seen"`
	IsOnline     bool     `json:"is_online"`
	OSType       string   `json:"os_type"`
	OSVersion    string   `json:"os_version"`
	AgentVersion string   `json:"agent_version"`
	Arch         string   `json:"arch"`
	Ips          []string `json:"ips,omitempty"`
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

// getFieldStrs 读取数组字段（如 ip_addresses）
func getFieldStrs(fields map[string]interface{}, key string) []string {
	v, ok := fields[key]
	if !ok {
		return nil
	}
	switch arr := v.(type) {
	case []interface{}:
		var out []string
		for _, item := range arr {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if arr != "" {
			return []string{arr}
		}
	}
	return nil
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

	if fp := getFieldStr(fields, "file_path"); fp != "" {
		return fp
	}
	if rip := getFieldStr(fields, "remote_ip"); rip != "" {
		port := getFieldUint(fields, "remote_port")
		lIP := getFieldStr(fields, "local_ip")
		lPort := getFieldUint(fields, "local_port")
		proc := getFieldStr(fields, "process_name")
		pid := getFieldUint(fields, "pid")
		// 格式: proc (PID) local:port → remote:port
		dest := fmt.Sprintf("%s:%d", rip, port)
		src := ""
		if lIP != "" {
			src = fmt.Sprintf("%s:%d → ", lIP, lPort)
		}
		if proc != "" && pid > 0 {
			return fmt.Sprintf("%s (PID %d) %s%s", proc, pid, src, dest)
		} else if proc != "" {
			return fmt.Sprintf("%s %s%s", proc, src, dest)
		}
		return fmt.Sprintf("%s%s", src, dest)
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
		if rip := getFieldStr(fields, "result_ips"); rip != "" {
			return fmt.Sprintf("%s → %s", qn, rip)
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
