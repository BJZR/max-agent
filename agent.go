package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var fenceRe = regexp.MustCompile("(?s)```(?:bash|sh|shell)?[ \t]*\n(.*?)```")

func fencedCommands(s string) []string {
	var out []string
	for _, m := range fenceRe.FindAllStringSubmatch(s, -1) {
		if c := strings.TrimSpace(m[1]); c != "" {
			out = append(out, c)
		}
	}
	return out
}

type Approver interface {
	Approve(name, args string) (bool, error)
}

type Agent struct {
	prov        *Provider
	config      Config
	system      string
	approver    Approver
	ws          *Workspace
	onToken     func(string)
	onReasoning func(string)
	onTool      func(name, args string)
	onToolOut   func(string)
}

const maxSteps = 12

var taskWords = []string{
	"haz", "hace", "hacer", "crea", "crear", "genera", "escribe", "escríbeme", "implementa",
	"instala", "arregla", "corrige", "corrigeme", "repara", "busca", "analiza", "investiga",
	"compila", "ejecuta", "corre", "prueba", "muestra", "verifica", "modifica", "actualiza",
	"agrega", "añade", "elimina", "borra", "quita", "cambia", "build", "install", "test",
}

func looksLikeTask(s string) bool {
	low := strings.ToLower(s)
	for _, w := range taskWords {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

const nudgeMsg = "No ejecutaste nada todavía. Si la petición toca la máquina, hazlo AHORA: envía los comandos en un bloque ```bash (primero explora con ls, cat, grep -n, rg; verifica antes de actuar). No repitas la explicación: ejecuta."

func (a *Agent) Chat(ctx context.Context, history []Msg, input string) (string, []Msg, error) {
	msgs := make([]Msg, 0, len(history)+maxSteps+2)
	msgs = append(msgs, Msg{Role: "system", Content: a.system})
	msgs = append(msgs, history...)
	msgs = append(msgs, Msg{Role: "user", Content: input})

	nudged := false
	anythingExecuted := false
	for step := 0; step < maxSteps; step++ {
		lastUser := input
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "user" {
				lastUser = msgs[i].Content
				break
			}
		}
		resp, err := a.prov.Chat(ctx, msgs, a.onToken, a.onReasoning)
		if err != nil {
			return "", msgs[1:], err
		}
		msgs = append(msgs, resp)
		if len(resp.ToolCalls) > 0 {
			anythingExecuted = true
			for _, tc := range resp.ToolCalls {
				name := tc.Function.Name
				args := tc.Function.Arguments
				if a.onTool != nil {
					a.onTool(name, args)
				}
				approved := true
				if a.approver != nil {
					var err error
					approved, err = a.approver.Approve(name, args)
					if err != nil {
						return "", msgs[1:], err
					}
				}
				if !approved {
					msgs = append(msgs, Msg{Role: "tool", Name: name, ToolCallID: tc.ID,
						Content: "El usuario rechazó ejecutar la herramienta. Continúa sin ejecutarla."})
					continue
				}
				out, err := execTool(ctx, a.ws, name, json.RawMessage(args))
				if a.onToolOut != nil {
					a.onToolOut(out)
				}
				if err != nil {
					out = "error: " + err.Error() + "\n" + out
				}
				msgs = append(msgs, Msg{Role: "tool", Name: name, ToolCallID: tc.ID, Content: out})
			}
			continue
		}
		if a.config.Tools {
			if cmds := fencedCommands(resp.Content); len(cmds) > 0 {
				var results strings.Builder
				executed := false
				for _, c := range cmds {
					args, _ := json.Marshal(map[string]any{"command": c, "timeout": "120s"})
					if a.onTool != nil {
						a.onTool("run_command", c)
					}
					if a.approver != nil {
						approved, err := a.approver.Approve("run_command", c)
						if err != nil {
							return "", msgs[1:], err
						}
						if !approved {
							results.WriteString("$ " + c + "\n(usuario rechazó)\n")
							continue
						}
					}
					out, err := execTool(ctx, a.ws, "run_command", args)
					if a.onToolOut != nil {
						a.onToolOut(out)
					}
					if err != nil {
						out = "error: " + err.Error() + "\n" + out
					}
					results.WriteString("$ " + c + "\n" + out + "\n")
					executed = true
					anythingExecuted = true
				}
				if executed {
					msgs = append(msgs, Msg{Role: "user",
						Content: "Resultado de los comandos que ejecutaste:\n" + strings.TrimSpace(results.String())})
					continue
				}
			}
		}
		if a.config.Tools && !nudged && !anythingExecuted && looksLikeTask(lastUser) && strings.TrimSpace(resp.Content) != "" {
			nudged = true
			msgs = append(msgs, Msg{Role: "user", Content: nudgeMsg})
			continue
		}
		return resp.Content, filterNudge(msgs[1:]), nil
	}
	return "", filterNudge(msgs[1:]), fmt.Errorf("límite de pasos de agente alcanzado (%d)", maxSteps)
}

func filterNudge(msgs []Msg) []Msg {
	out := make([]Msg, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "user" && m.Content == nudgeMsg {
			continue
		}
		out = append(out, m)
	}
	return out
}
