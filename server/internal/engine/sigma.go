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

	// 文件路径（调试用）
	filePath string
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

// LoadSigmaDir 从目录加载所有 .yml/.yaml 规则文件
func LoadSigmaDir(path string) ([]*SigmaRule, error) {
	var rules []*SigmaRule

	err := filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
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
			fmt.Fprintf(os.Stderr, "[Sigma] 跳过 %s: %v\n", p, err)
			return nil
		}
		rules = append(rules, rule)
		return nil
	})

	return rules, err
}

// LoadSigmaFile 加载单个 Sigma 规则文件
func LoadSigmaFile(path string) (*SigmaRule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取失败: %w", err)
	}

	var rule SigmaRule
	if err := yaml.Unmarshal(data, &rule); err != nil {
		return nil, fmt.Errorf("YAML 解析失败: %w", err)
	}
	rule.filePath = path

	if rule.Title == "" {
		return nil, fmt.Errorf("规则缺少 title")
	}
	if rule.Detection.Condition == "" {
		return nil, fmt.Errorf("规则缺少 detection.condition")
	}
	if len(rule.Detection.Selections) == 0 && len(rule.Detection.Keywords) == 0 {
		return nil, fmt.Errorf("规则缺少 selection")
	}

	return &rule, nil
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
