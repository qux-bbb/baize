// Elasticsearch 存储层 — 写入遥测事件和告警
package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"

	pb "github.com/qux-bbb/baize/proto/gen/go/baize/v1"
)

const (
	eventsIndexPrefix = "baize-events"
	alertsIndexPrefix = "baize-alerts"
)

// Store 封装 ES 操作
type Store struct {
	client *elasticsearch.Client
}

// New 创建 ES 存储实例
func New(address, username, password string) (*Store, error) {
	cfg := elasticsearch.Config{
		Addresses: []string{address},
		Username:  username,
		Password:  password,
	}
	client, err := elasticsearch.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("创建 ES 客户端失败: %w", err)
	}

	// 验证连接
	res, err := client.Info()
	if err != nil {
		return nil, fmt.Errorf("连接 ES 失败: %w", err)
	}
	defer res.Body.Close()
	if res.IsError() {
		return nil, fmt.Errorf("ES 返回错误: %s", res.String())
	}

	s := &Store{client: client}
	if err := s.ensureIndices(); err != nil {
		return nil, fmt.Errorf("确保索引存在失败: %w", err)
	}

	return s, nil
}

// ensureIndices 创建日期滚动索引模板（如果不存在）
func (s *Store) ensureIndices() error {
	ctx := context.Background()
	today := time.Now().Format("2006.01.02")

	indices := []string{
		fmt.Sprintf("%s-%s", eventsIndexPrefix, today),
		fmt.Sprintf("%s-%s", alertsIndexPrefix, today),
	}

	for _, idx := range indices {
		req := esapi.IndicesExistsRequest{Index: []string{idx}}
		res, err := req.Do(ctx, s.client)
		if err != nil {
			return fmt.Errorf("检查索引 %s 失败: %w", idx, err)
		}
		res.Body.Close()

		if res.StatusCode == 404 {
			mapping := getIndexMapping(idx)
			createReq := esapi.IndicesCreateRequest{
				Index: idx,
				Body:  bytes.NewReader(mapping),
			}
			createRes, err := createReq.Do(ctx, s.client)
			if err != nil {
				return fmt.Errorf("创建索引 %s 失败: %w", idx, err)
			}
			createRes.Body.Close()
			log.Printf("[ES] 索引 %s 已创建", idx)
		} else {
			log.Printf("[ES] 索引 %s 已存在", idx)
		}
	}
	return nil
}

// WriteEvent 写入一条 Agent 事件到 ES
func (s *Store) WriteEvent(event *pb.Event) error {
	body := buildEventDoc(event)
	return s.writeDoc(eventsIndexPrefix, event.GetSequenceId(), body)
}

// writeDoc 通用 ES 文档写入
func (s *Store) writeDoc(prefix string, seq uint64, body map[string]any) error {
	index := fmt.Sprintf("%s-%s", prefix, time.Now().Format("2006.01.02"))

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("JSON 序列化失败: %w", err)
	}

	req := esapi.IndexRequest{
		Index:   index,
		Body:    bytes.NewReader(jsonBody),
		Refresh: "false",
	}
	ctx := context.Background()
	res, err := req.Do(ctx, s.client)
	if err != nil {
		return fmt.Errorf("写入 ES 失败: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return fmt.Errorf("ES 写入错误: %s", res.String())
	}
	return nil
}

// Close 关闭 ES 连接（go-elasticsearch 自动管理连接池，此方法仅作占位）
func (s *Store) Close() {
	log.Println("[ES] 连接池已关闭")
}

// ── 辅助函数 ──────────────────────────────────────────────

// getIndexMapping 返回索引的 mapping 配置
func getIndexMapping(index string) []byte {
	// 用动态映射 + 禁用 text 的子字段（节省空间）
	mapping := map[string]any{
		"settings": map[string]any{
			"number_of_shards":   1,
			"number_of_replicas": 0,
		},
		"mappings": map[string]any{
			"dynamic": true,
			"_source": map[string]any{"enabled": true},
		},
	}
	b, _ := json.Marshal(mapping)
	return b
}

// buildEventDoc 将 Protobuf Event 转为 ES 文档结构
func buildEventDoc(event *pb.Event) map[string]any {
	doc := map[string]any{
		"@timestamp":   time.Now().UTC().Format(time.RFC3339Nano),
		"agent_id":     event.GetAgentInfo().GetAgentId(),
		"hostname":     event.GetAgentInfo().GetHostname(),
		"os_type":      event.GetAgentInfo().GetOsType(),
		"os_version":   event.GetAgentInfo().GetOsVersion(),
		"sequence_id":  event.GetSequenceId(),
		"event_type":   getEventTypeName(event),
	}

	// 根据事件类型填充详细字段
	switch e := event.GetEventType().(type) {
	case *pb.Event_ProcessCreate:
		doc["event_category"] = "process"
		doc["event_action"] = "create"
		doc["pid"] = e.ProcessCreate.GetPid()
		doc["parent_pid"] = e.ProcessCreate.GetParentPid()
		doc["command_line"] = e.ProcessCreate.GetCommandLine()
		doc["image_path"] = e.ProcessCreate.GetImagePath()
		doc["hash_sha256"] = e.ProcessCreate.GetHashSha256()
		doc["user"] = e.ProcessCreate.GetUser()
		doc["is_elevated"] = e.ProcessCreate.GetIsElevated()

	case *pb.Event_ProcessTerminate:
		doc["event_category"] = "process"
		doc["event_action"] = "terminate"
		doc["pid"] = e.ProcessTerminate.GetPid()
		doc["exit_code"] = e.ProcessTerminate.GetExitCode()

	case *pb.Event_FileCreate:
		doc["event_category"] = "file"
		doc["event_action"] = "create"
		doc["file_path"] = e.FileCreate.GetFilePath()
		doc["file_size"] = e.FileCreate.GetFileSize()
		doc["hash_sha256"] = e.FileCreate.GetHashSha256()

	case *pb.Event_FileModify:
		doc["event_category"] = "file"
		doc["event_action"] = "modify"
		doc["file_path"] = e.FileModify.GetFilePath()
		doc["file_size_before"] = e.FileModify.GetFileSizeBefore()
		doc["file_size_after"] = e.FileModify.GetFileSizeAfter()

	case *pb.Event_FileDelete:
		doc["event_category"] = "file"
		doc["event_action"] = "delete"
		doc["file_path"] = e.FileDelete.GetFilePath()
		doc["file_size"] = e.FileDelete.GetOriginalSize()
		doc["hash_sha256"] = e.FileDelete.GetHashSha256()

	case *pb.Event_NetworkConnection:
		doc["event_category"] = "network"
		doc["local_ip"] = e.NetworkConnection.GetLocalIp()
		doc["local_port"] = e.NetworkConnection.GetLocalPort()
		doc["remote_ip"] = e.NetworkConnection.GetRemoteIp()
		doc["remote_port"] = e.NetworkConnection.GetRemotePort()
		doc["protocol"] = e.NetworkConnection.GetProtocol()
		doc["direction"] = e.NetworkConnection.GetDirection()

	case *pb.Event_RegistryChange:
		doc["event_category"] = "registry"
		doc["event_action"] = e.RegistryChange.GetOperation()
		doc["registry_key"] = e.RegistryChange.GetKeyPath()
		doc["registry_value_name"] = e.RegistryChange.GetValueName()
		doc["registry_value_data"] = e.RegistryChange.GetValueData()

	case *pb.Event_ScheduledTask:
		doc["event_category"] = "scheduled_task"
		doc["event_action"] = e.ScheduledTask.GetOperation()
		doc["task_name"] = e.ScheduledTask.GetTaskName()
		doc["task_path"] = e.ScheduledTask.GetTaskPath()
		doc["trigger_type"] = e.ScheduledTask.GetTriggerType()

	case *pb.Event_YaraMatch:
		doc["event_category"] = "yara"
		doc["rule_name"] = e.YaraMatch.GetRuleName()
		doc["rule_id"] = e.YaraMatch.GetRuleId()
		doc["target_path"] = e.YaraMatch.GetTargetPath()
		doc["matched_string"] = e.YaraMatch.GetMatchedString()
		doc["scan_type"] = e.YaraMatch.GetScanType()
	}

	// 补充进程 pid（如果有）
	if pid := getEventPid(event); pid > 0 {
		doc["pid"] = pid
	}
	if proc := getEventProcessName(event); proc != "" {
		doc["process_name"] = proc
	}

	return doc
}

func getEventTypeName(event *pb.Event) string {
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
	}
	return "unknown"
}

func getEventPid(event *pb.Event) uint64 {
	switch e := event.GetEventType().(type) {
	case *pb.Event_ProcessCreate:
		return e.ProcessCreate.GetPid()
	case *pb.Event_ProcessTerminate:
		return e.ProcessTerminate.GetPid()
	case *pb.Event_FileCreate:
		return e.FileCreate.GetPid()
	case *pb.Event_FileModify:
		return e.FileModify.GetPid()
	case *pb.Event_FileDelete:
		return e.FileDelete.GetPid()
	case *pb.Event_NetworkConnection:
		return e.NetworkConnection.GetPid()
	case *pb.Event_RegistryChange:
		return e.RegistryChange.GetPid()
	case *pb.Event_ScheduledTask:
		return e.ScheduledTask.GetPid()
	}
	return 0
}

func getEventProcessName(event *pb.Event) string {
	switch e := event.GetEventType().(type) {
	case *pb.Event_FileCreate:
		return e.FileCreate.GetProcessName()
	case *pb.Event_FileModify:
		return e.FileModify.GetProcessName()
	case *pb.Event_FileDelete:
		return e.FileDelete.GetProcessName()
	case *pb.Event_NetworkConnection:
		return e.NetworkConnection.GetProcessName()
	case *pb.Event_RegistryChange:
		return e.RegistryChange.GetProcessName()
	case *pb.Event_ScheduledTask:
		return e.ScheduledTask.GetProcessName()
	}
	return ""
}
