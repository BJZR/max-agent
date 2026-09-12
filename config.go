package main

import (
	"fmt"
	"os"
	"os/user"
	"strings"

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
	Shell        string  `yaml:"shell"`
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
	if cfg.Shell == "" {
		cfg.Shell = resolveShell()
	}
	return cfg
}

// resolveShell devuelve la shell por defecto del usuario: $SHELL si está
// definida, si no la shell del /etc/passwd del usuario actual, si no /bin/sh.
func resolveShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	name := os.Getenv("USER")
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	if name != "" {
		if sh, ok := shellFromPasswd(name); ok {
			return sh
		}
	}
	return "/bin/sh"
}

func shellFromPasswd(name string) (string, bool) {
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Split(line, ":")
		if len(f) >= 7 && f[0] == name && f[6] != "" {
			return f[6], true
		}
	}
	return "", false
}
