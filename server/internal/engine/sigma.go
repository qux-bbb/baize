// Sigma 规则模型 — YAML 解析 + 匹配逻辑
package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SigmaRule 对应一条 Sigma YAML 规则文件
type SigmaRule struct {
	Title       string            `yaml:"title"`
	ID          string            `yaml:"id"`
	Description string            `yaml:"description"`
	Author      string            `yaml:"author"`
	Date        string            `yaml:"date"`
	Level       string            `yaml:"level"`
	LogSource   SigmaLogSource    `yaml:"logsource"`
	Detection   SigmaDetection    `yaml:"detection"`
	Fields      []string          `yaml:"fields"`
	FalsePos    []string          `yaml:"falsepositives"`
	Tags        []string          `yaml:"tags"`
	Status      string            `yaml:"status"`

	// FileName 规则文件名（不含目录）——规则的唯一标识，API / UI / 状态存储都用它
	FileName string `yaml:"-"`
}

// RuleMeta 规则展示元数据（Dashboard 规则页）
type RuleMeta struct {
	File        string   `json:"file"`
	Title       string   `json:"title"`
	ID          string   `json:"id"`
	Level       string   `json:"level"`
	Category    string   `json:"category"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Builtin     bool     `json:"builtin"`
	Enabled     bool     `json:"enabled"`
}

// RuleLoadError 规则解析失败信息（规则页需要明确告知哪条规则坏了、为什么）
type RuleLoadError struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

type SigmaLogSource struct {
	Category string `yaml:"category"`
	Product  string `yaml:"product"`
	Service  string `yaml:"service"`
}

type SigmaDetection struct {
	Keywords  map[string]interface{} `yaml:"-"` // selection 中非 map 的值（如字符串列表）
	Selections map[string]map[string]interface{} `yaml:",inline,flow"`
	Condition string                  `yaml:"condition"`
}

// 自定义 UnmarshalYAML 解析 detection 中的 selection 关键字
func (d *SigmaDetection) UnmarshalYAML(value *yaml.Node) error {
	// 先解成一个通用 map
	var raw map[string]interface{}
	if err := value.Decode(&raw); err != nil {
		return err
	}

	d.Selections = make(map[string]map[string]interface{})
	d.Keywords = make(map[string]interface{})

	for k, v := range raw {
		if k == "condition" {
			d.Condition, _ = v.(string)
			continue
		}
		switch val := v.(type) {
		case map[string]interface{}:
			d.Selections[k] = val
		default:
			// 非 map 类型的 selection（如 "selection: 1" 或字符串列表）
			d.Keywords[k] = val
		}
	}
	return nil
}

// LoadSigmaDirDetailed 从目录加载所有 .yml/.yaml 规则文件。
// 解析失败的文件不中断加载，收集到 errs 返回（供 Dashboard 展示哪条规则坏了）。
func LoadSigmaDirDetailed(path string) (rules []*SigmaRule, errs []RuleLoadError, err error) {
	err = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext != ".yml" && ext != ".yaml" {
			return nil
		}

		rule, err := LoadSigmaFile(p)
		if err != nil {
			errs = append(errs, RuleLoadError{File: filepath.Base(p), Error: err.Error()})
			return nil
		}
		rules = append(rules, rule)
		return nil
	})

	return rules, errs, err
}

// LoadSigmaFile 加载单个 Sigma 规则文件
func LoadSigmaFile(path string) (*SigmaRule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取失败: %w", err)
	}

	rule, err := ParseSigmaBytes(data)
	if err != nil {
		return nil, err
	}
	rule.FileName = filepath.Base(path)
	return rule, nil
}

// ParseSigmaBytes 解析并校验规则内容（不落盘，供上传/编辑时预校验）
func ParseSigmaBytes(data []byte) (*SigmaRule, error) {
	var rule SigmaRule
	if err := yaml.Unmarshal(data, &rule); err != nil {
		return nil, fmt.Errorf("YAML 解析失败: %w", err)
	}
	if err := validateRule(&rule); err != nil {
		return nil, err
	}
	return &rule, nil
}

// validateRule 规则必填项校验（title / detection.condition / selection）
func validateRule(rule *SigmaRule) error {
	if rule.Title == "" {
		return fmt.Errorf("规则缺少 title")
	}
	if rule.Detection.Condition == "" {
		return fmt.Errorf("规则缺少 detection.condition")
	}
	if len(rule.Detection.Selections) == 0 && len(rule.Detection.Keywords) == 0 {
		return fmt.Errorf("规则缺少 selection")
	}
	return nil
}

// SafeRuleFileName 校验规则文件名（防目录穿越 / 非法后缀）
func SafeRuleFileName(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("规则文件名不能为空")
	}
	if name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("规则文件名不能包含路径分隔符")
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".yml" && ext != ".yaml" {
		return "", fmt.Errorf("规则文件后缀必须是 .yml 或 .yaml")
	}
	if strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("规则文件名不合法")
	}
	return name, nil
}

// EventMap 将 Agent 事件转为字段键值对（用于规则匹配）
func EventToFields(eventType, category string, fields map[string]interface{}) map[string]string {
	result := make(map[string]string)
	result["event_type"] = eventType
	result["event_category"] = category

	for k, v := range fields {
		switch val := v.(type) {
		case string:
			result[k] = val
		case uint64:
			result[k] = fmt.Sprintf("%d", val)
		case bool:
			result[k] = fmt.Sprintf("%t", val)
		case int:
			result[k] = fmt.Sprintf("%d", val)
		default:
			result[k] = fmt.Sprintf("%v", val)
		}
	}
	return result
}

// RuleInfo 返回规则摘要信息
func (r *SigmaRule) RuleInfo() string {
	return fmt.Sprintf("[%s] %s (ID=%s, Level=%s)", r.LogSource.Category, r.Title, r.ID, r.Level)
}
