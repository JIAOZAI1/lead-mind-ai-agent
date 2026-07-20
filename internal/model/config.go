// Package model configures the ChatModel used by the Agent orchestration
// layer. Per PROJECT.md §1.3, the primary provider is a domestic model
// reached through an OpenAI-compatible endpoint (base_url/api_key
// configurable), with an overseas fallback chain to be added later
// (PROJECT.md §5 阶段三).
package model

import (
	"fmt"
	"os"
)

// Config holds the settings for the primary ChatModel provider.
type Config struct {
	// BaseURL is the OpenAI-compatible API endpoint, e.g. a domestic
	// provider's compatible-mode URL (Doubao/Ark, Qwen/DashScope, etc.).
	BaseURL string
	// APIKey authenticates against BaseURL.
	APIKey string
	// ModelName is the model identifier passed to the provider.
	ModelName string
}

// ConfigFromEnv reads the primary model config from environment variables:
// MODEL_BASE_URL, MODEL_API_KEY, MODEL_NAME.
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
