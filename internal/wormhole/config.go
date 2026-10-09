package wormhole

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type ServerConfig struct {
	Port int    `yaml:"port"`
	Name string `yaml:"name"`
}

type ProviderConfig struct {
	Name    string `yaml:"name"`
	BaseURL string `yaml:"base_url"` // optional when the name is a known preset
	KeyEnv  string `yaml:"key_env"`  // name of the env var holding the API key
	// ChatPath is appended to BaseURL for chat completions.
	// Default "/chat/completions"; set to "" when the base URL already
	// includes the endpoint (e.g. Pollinations /openai).
	ChatPath string `yaml:"chat_path"`
}

type ModelConfig struct {
	ID       string   `yaml:"id"`                // public model id, e.g. "llama-3.3-70b"
	Name     string   `yaml:"name"`              // display name, defaults to ID
	Provider string   `yaml:"provider"`          // provider name from the list above
	Upstream string   `yaml:"upstream"`          // upstream model id, defaults to ID
	Tags     []string `yaml:"tags"`              // free, paid, reasoning, local, demo...
	Context  int      `yaml:"context"`           // context window in tokens
	PriceIn  float64  `yaml:"price_in"`          // USD per 1M input tokens
	PriceOut float64  `yaml:"price_out"`         // USD per 1M output tokens
}

type LimitsConfig struct {
	// Default monthly spend limit (USD) pre-filled when creating keys in the dashboard. 0 = unlimited.
	DefaultKeyMonthlyLimit float64 `yaml:"default_key_monthly_limit"`
}

type Config struct {
	Server    ServerConfig     `yaml:"server"`
	Providers []ProviderConfig `yaml:"providers"`
	Models    []ModelConfig    `yaml:"models"`
	Limits    LimitsConfig     `yaml:"limits"`
}

// providerPresets: configs can reference a well-known provider by name alone.
// All "free tier" providers are included so config.free.yaml works out of the box.
var providerPresets = map[string]string{
	"groq":        "https://api.groq.com/openai/v1",
	"openai":      "https://api.openai.com/v1",
	"openrouter":  "https://openrouter.ai/api/v1",
	"together":    "https://api.together.xyz/v1",
	"deepseek":    "https://api.deepseek.com/v1",
	"mistral":     "https://api.mistral.ai/v1",
	"fireworks":   "https://api.fireworks.ai/inference/v1",
	"cerebras":    "https://api.cerebras.ai/v1",
	"xai":         "https://api.x.ai/v1",
	"ollama":      "http://127.0.0.1:11434/v1",
	"lmstudio":    "http://127.0.0.1:1234/v1",
	"google":      "https://generativelanguage.googleapis.com/v1beta/openai",
	"nvidia":      "https://integrate.api.nvidia.com/v1",
	"cohere":      "https://api.cohere.com/compatibility/v1",
	"sambanova":   "https://api.sambanova.ai/v1",
	"huggingface": "https://router.huggingface.co/v1",
	// keyless — no signup, no API key, works immediately
	"pollinations": "https://text.pollinations.ai/openai",
	"llm7":         "https://api.llm7.io/v1",
	"kilo":         "https://api.kilo.ai/api/gateway",
	"ovh":          "https://oai.endpoints.kepler.ai.cloud.ovh.net/v1",
}

// LoadConfig reads the YAML config, applying defaults and resolving
// provider presets. If config.yaml is missing it is created from
// config.example.yaml so first-run works with zero setup.
func LoadConfig(path string) (*Config, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		data, rerr := os.ReadFile("config.example.yaml")
		if rerr == nil {
			if werr := os.WriteFile(path, data, 0o644); werr == nil {
				fmt.Println("→ created config.yaml from config.example.yaml — edit it to add your models")
			}
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := applyDefaults(&cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyDefaults(cfg *Config) error {
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.Name == "" {
		cfg.Server.Name = "Wormhole"
	}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		p.Name = strings.TrimSpace(p.Name)
		p.BaseURL = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
		if p.BaseURL == "" && p.Name != "demo" {
			if url, ok := providerPresets[p.Name]; ok {
				p.BaseURL = url
			} else {
				return fmt.Errorf("provider %q: unknown preset — set base_url explicitly", p.Name)
			}
		}
		if p.ChatPath == "" && !strings.HasSuffix(p.BaseURL, "/openai") {
			p.ChatPath = "/chat/completions"
		}
	}
	for i := range cfg.Models {
		m := &cfg.Models[i]
		if m.Name == "" {
			m.Name = m.ID
		}
		if m.Upstream == "" {
			m.Upstream = m.ID
		}
	}
	return nil
}

func (c *Config) Validate() error {
	if len(c.Providers) == 0 {
		return fmt.Errorf("no providers configured — add at least one to config.yaml")
	}
	seenP := map[string]bool{}
	for _, p := range c.Providers {
		if p.Name == "" {
			return fmt.Errorf("provider with empty name")
		}
		if p.BaseURL == "" && p.Name != "demo" {
			return fmt.Errorf("provider %q: missing base_url", p.Name)
		}
		if seenP[p.Name] {
			return fmt.Errorf("duplicate provider %q", p.Name)
		}
		seenP[p.Name] = true
	}
	seenM := map[string]bool{}
	for _, m := range c.Models {
		if m.ID == "" {
			return fmt.Errorf("model with empty id")
		}
		if !seenP[m.Provider] {
			return fmt.Errorf("model %q references unknown provider %q", m.ID, m.Provider)
		}
		if seenM[m.ID] {
			return fmt.Errorf("duplicate model id %q", m.ID)
		}
		seenM[m.ID] = true
	}
	return nil
}

func (c *Config) ProviderByName(name string) *ProviderConfig {
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			return &c.Providers[i]
		}
	}
	return nil
}

func (c *Config) FindModel(id string) *ModelConfig {
	for i := range c.Models {
		if c.Models[i].ID == id {
			return &c.Models[i]
		}
	}
	return nil
}

// APIKey returns the provider's API key from the environment.
func (p *ProviderConfig) APIKey() string {
	if p.KeyEnv == "" {
		return ""
	}
	return os.Getenv(p.KeyEnv)
}

func (p *ProviderConfig) HasKey() bool {
	return p.KeyEnv == "" || p.APIKey() != ""
}

func (p *ProviderConfig) MissingKeyMsg() string {
	if p.KeyEnv == "" {
		return ""
	}
	return fmt.Sprintf("provider %q has no API key — add %s=<your key> to .env", p.Name, p.KeyEnv)
}

func (m *ModelConfig) IsFree() bool {
	return m.PriceIn == 0 && m.PriceOut == 0
}
