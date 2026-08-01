package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testConfig = `
app:
  name: file-app
  environment: test
http:
  host: 127.0.0.1
  port: 9000
  mode: test
  read_timeout: 2s
  write_timeout: 3s
  idle_timeout: 4s
  shutdown_timeout: 5s
`

func TestLoadReadsFileAndEnvironmentOverride(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(testConfig), 0o600); err != nil {
		t.Fatalf("写入测试配置文件失败: %v", err)
	}
	t.Setenv(configFileEnv, configPath)
	t.Setenv("LEAD_MIND_HTTP_PORT", "9100")
	t.Setenv("LEAD_MIND_OPENAI_API_KEY", "test-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 返回错误: %v", err)
	}
	if got, want := cfg.App.Name, "file-app"; got != want {
		t.Errorf("App.Name = %q，期望 %q", got, want)
	}
	if got, want := cfg.HTTP.Port, 9100; got != want {
		t.Errorf("HTTP.Port = %d，期望 %d", got, want)
	}
	if got, want := cfg.HTTP.ReadTimeout, 2*time.Second; got != want {
		t.Errorf("HTTP.ReadTimeout = %v，期望 %v", got, want)
	}
}

func TestLoadRejectsInvalidPort(t *testing.T) {
	t.Setenv(configFileEnv, "")
	t.Setenv("LEAD_MIND_HTTP_PORT", "70000")
	t.Setenv("LEAD_MIND_OPENAI_API_KEY", "test-key")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() 未拒绝无效端口")
	}
}

func TestLoadRejectsMissingAPIKey(t *testing.T) {
	t.Setenv(configFileEnv, "")
	t.Setenv("LEAD_MIND_OPENAI_API_KEY", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() 未拒绝缺失的 OpenAI API 密钥")
	}
}
