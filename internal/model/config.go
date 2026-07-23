// Package model 配置 Agent 编排层所使用的 ChatModel。根据 PROJECT.md
// §1.3，主用模型是通过 OpenAI 兼容协议接入的国内模型
// （base_url/api_key 可配置），海外备用链路后续再补充
// （PROJECT.md §5 阶段三）。
package model

import (
	"fmt"
	"os"
)

// Config 保存主用 ChatModel 供应商的配置。
type Config struct {
	// BaseURL 是 OpenAI 兼容协议的 API 地址，例如国内某供应商的兼容模式
	// URL（豆包/Ark、通义千问/DashScope 等）。
	BaseURL string
	// APIKey 用于访问 BaseURL 的鉴权凭证。
	APIKey string
	// ModelName 是传给供应商的模型标识。
	ModelName string
}

// ConfigFromEnv 从环境变量读取主用模型配置：
// MODEL_BASE_URL、MODEL_API_KEY、MODEL_NAME。
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		BaseURL:   os.Getenv("MODEL_BASE_URL"),
		APIKey:    os.Getenv("MODEL_API_KEY"),
		ModelName: os.Getenv("MODEL_NAME"),
	}

	if cfg.BaseURL == "" {
		return Config{}, fmt.Errorf("MODEL_BASE_URL is required")
	}
	if cfg.ModelName == "" {
		return Config{}, fmt.Errorf("MODEL_NAME is required")
	}

	return cfg, nil
}
