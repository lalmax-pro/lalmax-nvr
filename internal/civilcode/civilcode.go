// Package civilcode 提供 GB/T 2260 行政区划编码的内置查询能力。
//
// 数据来自内嵌的 civilCode.csv (编号,名称,上级)，用于行政区划节点的
// 名称补全、父链解析与层级描述。无外部依赖，可被 storage / gb28181 / api 复用。
package civilcode

import (
	"bufio"
	"bytes"
	_ "embed"
	"strings"
	"sync"
)

//go:embed civilCode.csv
var csvData []byte

// Code 表示一条行政区划记录。
type Code struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	ParentCode string `json:"parent_code"`
}

var (
	once  sync.Once
	table map[string]Code
)

func load() {
	table = make(map[string]Code, 3500)
	scanner := bufio.NewScanner(bytes.NewReader(csvData))
	first := true
	for scanner.Scan() {
		line := scanner.Text()
		if first { // 跳过表头
			first = false
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 2 {
			continue
		}
		code := strings.TrimSpace(parts[0])
		if code == "" {
			continue
		}
		c := Code{Code: code, Name: strings.TrimSpace(parts[1])}
		if len(parts) >= 3 {
			c.ParentCode = strings.TrimSpace(parts[2])
		}
		table[code] = c
	}
}

func data() map[string]Code {
	once.Do(load)
	return table
}

// Get 返回指定编码的记录。
func Get(code string) (Code, bool) {
	c, ok := data()[code]
	return c, ok
}

// Loaded 报告内置表是否成功加载。
func Loaded() bool {
	return len(data()) > 0
}

// Size 返回内置表条目数。
func Size() int {
	return len(data())
}

// Parent 返回父级记录。优先用记录自带的 ParentCode，
// 缺失时按 GB/T 2260 长度规则 (8→6, 6→4, 4→2) 回退。
func Parent(code string) (Code, bool) {
	tbl := data()
	if c, ok := tbl[code]; ok && c.ParentCode != "" {
		if p, ok := tbl[c.ParentCode]; ok {
			return p, true
		}
	}
	pc := parentByLength(code)
	if pc == "" {
		return Code{}, false
	}
	p, ok := tbl[pc]
	return p, ok
}

// AllParents 返回从最近父级到根的父链 (近→远)。
func AllParents(code string) []Code {
	var chain []Code
	cur := code
	for {
		p, ok := Parent(cur)
		if !ok {
			break
		}
		chain = append(chain, p)
		cur = p.Code
	}
	return chain
}

// Children 返回某父编码下的直接子级; parent 为空返回顶层(省级)。
func Children(parent string) []Code {
	var result []Code
	for _, c := range data() {
		if parent == "" {
			if c.ParentCode == "" {
				result = append(result, c)
			}
		} else if c.ParentCode == parent {
			result = append(result, c)
		}
	}
	return result
}

// Description 返回完整层级描述, 如 "广东省/广州市/天河区"。
func Description(code string) string {
	c, ok := Get(code)
	if !ok {
		return ""
	}
	parts := []string{c.Name}
	for _, p := range AllParents(code) {
		parts = append([]string{p.Name}, parts...)
	}
	return strings.Join(parts, "/")
}

func parentByLength(code string) string {
	switch {
	case len(code) >= 8:
		return code[:6]
	case len(code) >= 6:
		return code[:4]
	case len(code) >= 4:
		return code[:2]
	default:
		return ""
	}
}
