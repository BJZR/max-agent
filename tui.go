package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

type termApprover struct {
	in   *bufio.Scanner
	out  io.Writer
	auto bool
}

func (t *termApprover) Approve(name, args string) (bool, error) {
	if !dangerous[name] || t.auto {
		return true, nil
	}
	fmt.Fprintf(t.out, "\033[33m▸ herramienta %s\033[0m\n", name)
	fmt.Fprintf(t.out, "  %s\n", truncate(args, 400))
	fmt.Fprintf(t.out, "¿aprobar? [y/N] ")
	if !t.in.Scan() {
		return false, io.EOF
	}
	switch strings.ToLower(strings.TrimSpace(t.in.Text())) {
	case "y", "yes", "s", "si", "aprobar":
		return true, nil
	}
	return false, nil
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func runTUI(cfg Config, prov *Provider, system string) error {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	out := os.Stdout

	ws := newWorkspace()

	agent := &Agent{
		prov:        prov,
		config:      cfg,
		system:      system,
		ws:          ws,
		approver:    &termApprover{in: sc, out: out, auto: cfg.AutoApprove || !isTTY(os.Stdin)},
		onToken:     func(t string) { fmt.Fprint(out, t) },
		onReasoning: func(r string) { fmt.Fprintf(out, "\033[2m%s\033[0m", r) },
		onTool: func(name, args string) {
			fmt.Fprintf(out, "\033[36m▸ %s: %s\033[0m\n", name, truncate(args, 300))
		},
		onToolOut: func(s string) {
			if s != "" {
				fmt.Fprintf(out, "\033[2m%s\033[0m\n", truncate(s, 1000))
			}
		},
	}

	fmt.Fprintf(out, "\033[1;32mMAX\033[0m — agente minimalista · modelo \033[36m%s\033[0m · /help\n", cfg.Model)
	history := []Msg{}
	seed := func() []Msg {
		if cfg.Context && cfg.Tools {
			return []Msg{{Role: "user", Content: "[Contexto del entorno (dado por MAX)]\n" + envSnapshot()}}
		}
		return nil
	}
	saveSession := func() {
		if cfg.Persist {
			st := loadStore()
			st["tui"] = sessionData{Updated: time.Now(), Cwd: ws.Cwd(), Messages: filterNudge(history)}
			if err := st.Save(); err != nil {
				fmt.Fprintf(out, "\033[31m(persistir: %v)\033[0m\n", err)
			}
		}
	}
	history = seed()
	if cfg.Persist {
		if d, ok := loadStore()["tui"]; ok && len(d.Messages) > 0 {
			var fresh []Msg
			for _, m := range d.Messages {
				if strings.Contains(m.Content, "[Contexto del entorno (dado por MAX)]") {
					continue
				}
				fresh = append(fresh, m)
			}
			history = append(seed(), fresh...)
			if d.Cwd != "" {
				ws.SetCwd(d.Cwd)
				fmt.Fprintf(out, "\033[2m(restaurada sesión · cwd %s)\033[0m\n", ws.Cwd())
			}
		}
	}
	for {
		fmt.Fprintf(out, "\033[36m»\033[0m ")
		if !sc.Scan() {
			break
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		switch {
		case line == "/exit" || line == "/quit":
			saveSession()
			return nil
		case line == "/clear":
			ws.SetCwd(newWorkspace().Cwd())
			history = seed()
			saveSession()
			fmt.Fprintln(out)
			continue
		case line == "/help":
			printHelp(out)
			continue
		case line == "/tools":
			for _, t := range toolSpecs {
				fmt.Fprintf(out, "  \033[36m%-14s\033[0m %s\n", t.Function.Name, t.Function.Description)
			}
			fmt.Fprintln(out)
			continue
		}
		fmt.Fprintf(out, "\033[36m»\033[0m %s\n", truncate(line, 500))
		content, newHist, err := agent.Chat(context.Background(), history, line)
		history = newHist
		saveSession()
		fmt.Fprintln(out)
		if err != nil {
			fmt.Fprintf(out, "\033[31merror: %s\033[0m\n", err)
		} else if content == "" {
			fmt.Fprintf(out, "\033[31m(sin respuesta)\033[0m\n")
		}
	}
	return nil
}

func printHelp(out io.Writer) {
	fmt.Fprint(out, `comandos:
  /help    esta ayuda
  /tools   lista de herramientas
  /clear   borra el historial
  /exit    salir

tips:
  Ctrl+D para salir · todo lo demás se envía al modelo
`)
}
