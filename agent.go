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
	msgs = append(msgs, condense(history, ctxCharsBudget)...)
	msgs = append(msgs, Msg{Role: "user", Content: input})

	nudged := false
	anythingExecuted := false
	attempted := map[string]bool{}
	for step := 0; step < maxSteps; step++ {
		msgs = condense(msgs, ctxCharsBudget)
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
				failed := false
				for _, c := range cmds {
					if attempted[c] {
						results.WriteString("$ " + c + "\n(BLOQUEADO: este comando ya falló y no se re-ejecuta. Estás en " + a.ws.Cwd() + ". Probá un comando NUEVO y más simple.)\n")
						failed = true
						executed = true
						continue
					}
					attempted[c] = true
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
						failed = true
					}
					results.WriteString("$ " + c + "\n" + out + "\n")
					executed = true
					anythingExecuted = true
				}
				if executed {
					msg := "Resultado de los comandos que ejecutaste:\n" + strings.TrimSpace(results.String())
					if failed {
						msg += "\n\nAl menos un comando falló. Estás en " + a.ws.Cwd() + ". No repitas el comando que falló: empezá por el PRIMER error. Comandos CORTOS, de a uno: si falta la carpeta, creala (mkdir -p). Si falta un header/import, agregalo antes de compilar. Reintentá con un comando distinto."
					}
					msgs = append(msgs, Msg{Role: "user", Content: msg})
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

const ctxCharsBudget = 16000

func condense(msgs []Msg, budget int) []Msg {
	if len(msgs) == 0 {
		return msgs
	}
	head := []Msg{msgs[0]}
	if len(msgs) > 1 && msgs[1].Role == "user" && strings.HasPrefix(msgs[1].Content, "[Contexto del entorno") {
		head = append(head, msgs[1])
	}
	total := 0
	for _, m := range head {
		total += len(m.Content)
	}
	var tail []Msg
	for i := len(msgs) - 1; i >= len(head); i-- {
		m := msgs[i]
		if total+len(m.Content) > budget && len(tail) >= 2 {
			break
		}
		total += len(m.Content)
		tail = append([]Msg{m}, tail...)
	}
	out := make([]Msg, 0, len(msgs))
	out = append(out, head...)
	if len(head)+len(tail) < len(msgs) {
		out = append(out, Msg{Role: "user",
			Content: "Parte de la conversación anterior se omitió por el límite de contexto. Continuá con la tarea usando lo último que se dijo."})
	}
	out = append(out, tail...)
	return out
}
