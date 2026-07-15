// 检测引擎 — 内置 Sigma 规则（Go embed）
package engine

import (
	"embed"
)

//go:embed rules/*.yml
var rulesFS embed.FS

// BuiltInRuleNames 返回内嵌规则文件名列表
func BuiltInRuleNames() ([]string, error) {
	entries, err := rulesFS.ReadDir("rules")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// ReadBuiltInRule 读取内嵌规则内容
func ReadBuiltInRule(name string) ([]byte, error) {
	return rulesFS.ReadFile("rules/" + name)
}
