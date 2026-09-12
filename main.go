package main

import (
	"embed"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
)

//go:embed static
var staticFS embed.FS

func main() {
	var (
		mode      = flag.String("mode", "tui", "interfaz: tui | web")
		config    = flag.String("config", "max.yaml", "archivo de configuración")
		model     = flag.String("model", "", "modelo (sobreescribe config)")
		base      = flag.String("base-url", "", "URL base de la API (/v1)")
		apiKey    = flag.String("api-key", "", "clave de API")
		server    = flag.String("server", "", "dirección del servidor web")
		yes       = flag.Bool("yes", false, "auto-aprobar herramientas peligrosas")
		noTool    = flag.Bool("no-tools", false, "desactivar herramientas")
		noPersist = flag.Bool("no-persist", false, "no guardar sesiones en disco")
		noMemory  = flag.Bool("no-memory", false, "desactivar memoria persistente")
		shell     = flag.String("shell", "", "shell para ejecutar comandos (por defecto la del usuario)")
		help      = flag.Bool("help", false, "mostrar ayuda")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `MAX — agente minimalista en Go

uso: max [flags]

  -mode tui|web      interfaz (default: tui)
  -config ARCHIVO    configuración YAML (default: max.yaml)
  -model NOMBRE      modelo (sobreescribe config)
  -base-url URL      URL base de la API, ej http://localhost:8080/v1
  -api-key CLAVE     clave API (solo APIs remotas)
  -server DIR        dirección del modo web, ej :8090
  -yes[bool]         aprobar herramientas sin preguntar
  -no-tools[bool]    desactivar herramientas
  -no-persist[bool]  no guardar sesiones en disco (~/.max/sessions.json)
  -no-memory[bool]   desactivar memoria persistente (~/.max/memory.md)
  -help              esta ayuda

ejemplos:
  max                               # tui contra llama-server local
  max -mode web -server :9000       # interfaz web en http://localhost:9000
  max -model openrouter/auto -base-url https://openrouter.ai/api/v1 -api-key $KEY
`)
	}
	flag.Parse()

	if *help {
		flag.Usage()
		return
	}

	cfg, err := loadConfig(*config)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *model != "" {
		cfg.Model = *model
	}
	if *base != "" {
		cfg.BaseURL = *base
	}
	if *apiKey != "" {
		cfg.APIKey = *apiKey
	}
	if *server != "" {
		cfg.ServerAddr = *server
	}
	if *yes {
		cfg.AutoApprove = true
	}
	if *noTool {
		cfg.Tools = false
	}
	if *noPersist {
		cfg.Persist = false
	}
	if *noMemory {
		cfg.Memory = false
	}
	if *shell != "" {
		cfg.Shell = *shell
	}

	system := defaultSystemPrompt
	if data, err := os.ReadFile(cfg.SystemPrompt); err == nil && strings.TrimSpace(string(data)) != "" {
		system = strings.TrimSpace(string(data))
	}

	prov := NewProvider(cfg)

	switch *mode {
	case "web", "serve":
		runWeb(cfg, prov, system)
	case "tui", "term", "cli", "":
		if err := runTUI(cfg, prov, system); err != nil {
			log.Fatal(err)
		}
	default:
		fmt.Fprintf(os.Stderr, "modo desconocido: %s (tui | web)\n", *mode)
		os.Exit(1)
	}
}
