// 事件字段提取 — 将 Protobuf Event 转为 map[string]string 供规则匹配
package engine

import (
	"fmt"

	pb "github.com/qux-bbb/baize/proto/gen/go/baize/v1"
)

// EventToFieldMap 将 Protobuf Event 转为扁平字段映射
func EventToFieldMap(event *pb.Event) map[string]string {
	m := make(map[string]string)

	// Agent 元信息
	m["agent_id"] = event.GetAgentInfo().GetAgentId()
	m["hostname"] = event.GetAgentInfo().GetHostname()
	m["os_type"] = event.GetAgentInfo().GetOsType()
	m["os_version"] = event.GetAgentInfo().GetOsVersion()
	m["sequence_id"] = fmt.Sprintf("%d", event.GetSequenceId())

	// 事件类型和分类
	m["event_type"] = getEVType(event)
	m["event_category"] = getEVCategory(event)

	// 事件特有字段
	switch e := event.GetEventType().(type) {
	case *pb.Event_ProcessCreate:
		m["pid"] = fmt.Sprintf("%d", e.ProcessCreate.GetPid())
		m["parent_pid"] = fmt.Sprintf("%d", e.ProcessCreate.GetParentPid())
		m["command_line"] = e.ProcessCreate.GetCommandLine()
		m["image_path"] = e.ProcessCreate.GetImagePath()
		m["hash_sha256"] = e.ProcessCreate.GetHashSha256()
		m["user"] = e.ProcessCreate.GetUser()

	case *pb.Event_ProcessTerminate:
		m["pid"] = fmt.Sprintf("%d", e.ProcessTerminate.GetPid())

	case *pb.Event_FileCreate:
		m["file_path"] = e.FileCreate.GetFilePath()
		m["file_size"] = fmt.Sprintf("%d", e.FileCreate.GetFileSize())
		m["hash_sha256"] = e.FileCreate.GetHashSha256()
		m["pid"] = fmt.Sprintf("%d", e.FileCreate.GetPid())
		m["process_name"] = e.FileCreate.GetProcessName()

	case *pb.Event_FileModify:
		m["file_path"] = e.FileModify.GetFilePath()
		m["pid"] = fmt.Sprintf("%d", e.FileModify.GetPid())
		m["process_name"] = e.FileModify.GetProcessName()

	case *pb.Event_FileDelete:
		m["file_path"] = e.FileDelete.GetFilePath()
		m["file_size"] = fmt.Sprintf("%d", e.FileDelete.GetOriginalSize())
		m["hash_sha256"] = e.FileDelete.GetHashSha256()
		m["pid"] = fmt.Sprintf("%d", e.FileDelete.GetPid())
		m["process_name"] = e.FileDelete.GetProcessName()

	case *pb.Event_NetworkConnection:
		m["local_ip"] = e.NetworkConnection.GetLocalIp()
		m["local_port"] = fmt.Sprintf("%d", e.NetworkConnection.GetLocalPort())
		m["remote_ip"] = e.NetworkConnection.GetRemoteIp()
		m["remote_port"] = fmt.Sprintf("%d", e.NetworkConnection.GetRemotePort())
		m["protocol"] = e.NetworkConnection.GetProtocol()
		m["direction"] = e.NetworkConnection.GetDirection()
		m["pid"] = fmt.Sprintf("%d", e.NetworkConnection.GetPid())
		m["process_name"] = e.NetworkConnection.GetProcessName()

	case *pb.Event_RegistryChange:
		m["registry_key"] = e.RegistryChange.GetKeyPath()
		m["registry_value_name"] = e.RegistryChange.GetValueName()
		m["registry_value_data"] = e.RegistryChange.GetValueData()
		m["pid"] = fmt.Sprintf("%d", e.RegistryChange.GetPid())
		m["process_name"] = e.RegistryChange.GetProcessName()

	case *pb.Event_ScheduledTask:
		m["task_name"] = e.ScheduledTask.GetTaskName()
		m["task_path"] = e.ScheduledTask.GetTaskPath()
		m["trigger_type"] = e.ScheduledTask.GetTriggerType()
		m["pid"] = fmt.Sprintf("%d", e.ScheduledTask.GetPid())
		m["process_name"] = e.ScheduledTask.GetProcessName()

	case *pb.Event_YaraMatch:
		m["rule_name"] = e.YaraMatch.GetRuleName()
		m["rule_id"] = e.YaraMatch.GetRuleId()
		m["target_path"] = e.YaraMatch.GetTargetPath()
		m["scan_type"] = e.YaraMatch.GetScanType()
	}

	return m
}

func getEVType(event *pb.Event) string {
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

func getEVCategory(event *pb.Event) string {
	switch event.GetEventType().(type) {
	case *pb.Event_ProcessCreate, *pb.Event_ProcessTerminate:
		return "process"
	case *pb.Event_FileCreate, *pb.Event_FileModify, *pb.Event_FileDelete:
		return "file"
	case *pb.Event_NetworkConnection:
		return "network"
	case *pb.Event_RegistryChange:
		return "registry"
	case *pb.Event_ScheduledTask:
		return "scheduled_task"
	case *pb.Event_YaraMatch:
		return "yara"
	}
	return "unknown"
}
