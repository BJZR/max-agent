package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Model        string  `yaml:"model"`
	BaseURL      string  `yaml:"base_url"`
	APIKey       string  `yaml:"api_key"`
	SystemPrompt string  `yaml:"system_prompt"`
	NumCtx       int     `yaml:"num_ctx"`
	Temperature  float64 `yaml:"temperature"`
	Tools        bool    `yaml:"tools"`
	Context      bool    `yaml:"context"`
	AutoApprove  bool    `yaml:"auto_approve"`
	Persist      bool    `yaml:"persist"`
	ServerAddr   string  `yaml:"server_addr"`
}

func defaultConfig() Config {
	return Config{
		Model:        "qwen2.5-coder:7b",
		BaseURL:      "http://localhost:8080/v1",
		SystemPrompt: "prompts/default.md",
		NumCtx:       8192,
		Temperature:  0.2,
		Tools:        true,
		Context:      true,
		AutoApprove:  false,
		Persist:      true,
		ServerAddr:   ":8090",
	}
}

func loadConfig(path string) (Config, error) {
	cfg := defaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %v", path, err)
	}
	return normalize(cfg), nil
}

func normalize(cfg Config) Config {
	base := defaultConfig()
	if cfg.Model == "" {
		cfg.Model = base.Model
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = base.BaseURL
	}
	if cfg.SystemPrompt == "" {
		cfg.SystemPrompt = base.SystemPrompt
	}
	if cfg.NumCtx == 0 {
		cfg.NumCtx = base.NumCtx
	}
	if cfg.Temperature == 0 {
		cfg.Temperature = base.Temperature
	}
	if cfg.ServerAddr == "" {
		cfg.ServerAddr = base.ServerAddr
	}
	return cfg
}
