// Package config 负责加载和校验应用配置。
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const (
	configFileEnv = "LEAD_MIND_CONFIG_FILE"
	envPrefix     = "LEAD_MIND"
)

// Config 表示应用启动所需的完整配置。
type Config struct {
	App    AppConfig    `mapstructure:"app"`
	HTTP   HTTPConfig   `mapstructure:"http"`
	OpenAI OpenAIConfig `mapstructure:"openai"`
	Agent  AgentConfig  `mapstructure:"agent"`
}

// AppConfig 表示应用自身的基础配置。
type AppConfig struct {
	Name        string `mapstructure:"name"`
	Environment string `mapstructure:"environment"`
}

// HTTPConfig 表示 HTTP 服务的监听地址和超时配置。
type HTTPConfig struct {
	Host              string        `mapstructure:"host"`
	Port              int           `mapstructure:"port"`
	Mode              string        `mapstructure:"mode"`
	ReadHeaderTimeout time.Duration `mapstructure:"read_header_timeout"`
	ReadTimeout       time.Duration `mapstructure:"read_timeout"`
	WriteTimeout      time.Duration `mapstructure:"write_timeout"`
	IdleTimeout       time.Duration `mapstructure:"idle_timeout"`
	ShutdownTimeout   time.Duration `mapstructure:"shutdown_timeout"`
}

// OpenAIConfig 表示 OpenAI 或 OpenAI 兼容服务的模型配置。
type OpenAIConfig struct {
	APIKey  string        `mapstructure:"api_key"`
	BaseURL string        `mapstructure:"base_url"`
	Model   string        `mapstructure:"model"`
	Timeout time.Duration `mapstructure:"timeout"`
}

// AgentConfig 表示 Eino ChatModelAgent 的运行配置。
type AgentConfig struct {
	Name           string `mapstructure:"name"`
	Description    string `mapstructure:"description"`
	Instruction    string `mapstructure:"instruction"`
	MaxIterations  int    `mapstructure:"max_iterations"`
	MaxInputLength int    `mapstructure:"max_input_length"`
}

// Address 返回符合 net/http 要求的监听地址。
func (c HTTPConfig) Address() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// Load 按照“环境变量、配置文件、默认值”的优先级加载配置。
func Load() (Config, error) {
	v := viper.New()
	setDefaults(v)
	configureEnvironment(v)

	if err := readConfigFile(v); err != nil {
		return Config{}, err
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("解析配置: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// Validate 校验启动 HTTP 服务所需的配置约束。
func (c Config) Validate() error {
	if strings.TrimSpace(c.App.Name) == "" {
		return errors.New("配置 app.name 不能为空")
	}
	if err := validateHTTP(c.HTTP); err != nil {
		return err
	}
	if err := validateOpenAI(c.OpenAI); err != nil {
		return err
	}
	return validateAgent(c.Agent)
}

func validateHTTP(cfg HTTPConfig) error {
	if cfg.Port < 1 || cfg.Port > 65535 {
		return fmt.Errorf("配置 http.port 必须在 1 到 65535 之间，当前值为 %d", cfg.Port)
	}
	if cfg.Mode != "debug" && cfg.Mode != "release" && cfg.Mode != "test" {
		return fmt.Errorf("配置 http.mode 只能是 debug、release 或 test，当前值为 %q", cfg.Mode)
	}
	if cfg.ReadHeaderTimeout <= 0 || cfg.ReadTimeout <= 0 || cfg.WriteTimeout < 0 || cfg.IdleTimeout <= 0 {
		return errors.New("HTTP 请求头、读和空闲超时必须大于 0，写超时不能小于 0")
	}
	if cfg.ShutdownTimeout <= 0 {
		return errors.New("配置 http.shutdown_timeout 必须大于 0")
	}
	return nil
}

func validateOpenAI(cfg OpenAIConfig) error {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return errors.New("配置 openai.api_key 不能为空")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return errors.New("配置 openai.model 不能为空")
	}
	if cfg.Timeout <= 0 {
		return errors.New("配置 openai.timeout 必须大于 0")
	}
	return nil
}

func validateAgent(cfg AgentConfig) error {
	if strings.TrimSpace(cfg.Name) == "" || strings.TrimSpace(cfg.Instruction) == "" {
		return errors.New("配置 agent.name 和 agent.instruction 不能为空")
	}
	if cfg.MaxIterations <= 0 || cfg.MaxInputLength <= 0 {
		return errors.New("配置 agent.max_iterations 和 agent.max_input_length 必须大于 0")
	}
	return nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("app.name", "lead-mind-ai-agent")
	v.SetDefault("app.environment", "development")
	v.SetDefault("http.host", "0.0.0.0")
	v.SetDefault("http.port", 8080)
	v.SetDefault("http.mode", "debug")
	v.SetDefault("http.read_header_timeout", "5s")
	v.SetDefault("http.read_timeout", "10s")
	// SSE 响应持续时间不固定，因此默认禁用整个 HTTP Server 的写超时。
	v.SetDefault("http.write_timeout", "0s")
	v.SetDefault("http.idle_timeout", "60s")
	v.SetDefault("http.shutdown_timeout", "10s")
	v.SetDefault("openai.api_key", "")
	v.SetDefault("openai.base_url", "")
	v.SetDefault("openai.model", "gpt-5.6-sol")
	v.SetDefault("openai.timeout", "120s")
	v.SetDefault("agent.name", "lead-mind-assistant")
	v.SetDefault("agent.description", "面向 SaaS 场景提供智能问答能力")
	v.SetDefault("agent.instruction", "你是 lead-mind-ai-agent 的智能助手。请使用中文清晰、准确地回答用户问题。")
	v.SetDefault("agent.max_iterations", 10)
	v.SetDefault("agent.max_input_length", 8000)
}

func configureEnvironment(v *viper.Viper) {
	// 将点号和下划线都映射为环境变量下划线，便于表达嵌套配置键。
	v.SetEnvPrefix(envPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
}

func readConfigFile(v *viper.Viper) error {
	configFile := strings.TrimSpace(os.Getenv(configFileEnv))
	if configFile != "" {
		v.SetConfigFile(configFile)
		if err := v.ReadInConfig(); err != nil {
			return fmt.Errorf("读取配置文件 %q: %w", configFile, err)
		}
		return nil
	}

	// 默认配置文件是可选的，保证容器环境可以仅通过环境变量启动。
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath("./configs")
	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if errors.As(err, &notFound) {
			return nil
		}
		return fmt.Errorf("读取默认配置文件: %w", err)
	}

	return nil
}
