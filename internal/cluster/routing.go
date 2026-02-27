// Package cluster 处理多节点路由表管理。
package cluster

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const routingCacheFile = "routing-cache.json"

// RoutingEntry 单个 s-proxy 节点的路由信息。
type RoutingEntry struct {
	ID      string `json:"id"`
	Addr    string `json:"addr"`
	Weight  int    `json:"weight"`
	Healthy bool   `json:"healthy"`
}

// RoutingTable 路由表（版本化）。
// Version 单调递增；c-proxy 保存本地版本，响应头版本更大时则更新。
type RoutingTable struct {
	Version int64          `json:"version"`
	Entries []RoutingEntry `json:"entries"`
}

// Encode 将路由表序列化为 Base64+JSON 字符串（用于放入响应头）。
func (rt *RoutingTable) Encode() (string, error) {
	data, err := json.Marshal(rt)
	if err != nil {
		return "", fmt.Errorf("routing table marshal: %w", err)
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// DecodeRoutingTable 从 Base64+JSON 字符串解析路由表。
func DecodeRoutingTable(encoded string) (*RoutingTable, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("routing table base64 decode: %w", err)
	}
	var rt RoutingTable
	if err := json.Unmarshal(data, &rt); err != nil {
		return nil, fmt.Errorf("routing table unmarshal: %w", err)
	}
	return &rt, nil
}

// SaveToDir 将路由表持久化到指定目录下的 routing-cache.json。
func (rt *RoutingTable) SaveToDir(dir string) error {
	data, err := json.MarshalIndent(rt, "", "  ")
	if err != nil {
		return fmt.Errorf("routing table marshal for save: %w", err)
	}
	path := filepath.Join(dir, routingCacheFile)
	return os.WriteFile(path, data, 0o600)
}

// LoadFromDir 从指定目录下的 routing-cache.json 加载路由表。
// 文件不存在时返回 nil, nil（无缓存）。
func LoadFromDir(dir string) (*RoutingTable, error) {
	path := filepath.Join(dir, routingCacheFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("routing cache read: %w", err)
	}
	var rt RoutingTable
	if err := json.Unmarshal(data, &rt); err != nil {
		return nil, fmt.Errorf("routing cache unmarshal: %w", err)
	}
	return &rt, nil
}
