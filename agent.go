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
	onToken     func(string)
	onReasoning func(string)
	onTool      func(name, args string)
	onToolOut   func(string)
}

const maxSteps = 12

func (a *Agent) Chat(ctx context.Context, history []Msg, input string) (string, []Msg, error) {
	msgs := make([]Msg, 0, len(history)+maxSteps+2)
	msgs = append(msgs, Msg{Role: "system", Content: a.system})
	msgs = append(msgs, history...)
	msgs = append(msgs, Msg{Role: "user", Content: input})

	for step := 0; step < maxSteps; step++ {
		resp, err := a.prov.Chat(ctx, msgs, a.onToken, a.onReasoning)
		if err != nil {
			return "", msgs[1:], err
		}
		msgs = append(msgs, resp)
		if len(resp.ToolCalls) > 0 {
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
				out, err := execTool(ctx, name, json.RawMessage(args))
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
					out, err := execTool(ctx, "run_command", args)
					if a.onToolOut != nil {
						a.onToolOut(out)
					}
					if err != nil {
						out = "error: " + err.Error() + "\n" + out
					}
					results.WriteString("$ " + c + "\n" + out + "\n")
					executed = true
				}
				if executed {
					msgs = append(msgs, Msg{Role: "user",
						Content: "Resultado de los comandos que ejecutaste:\n" + strings.TrimSpace(results.String())})
					continue
				}
			}
		}
		return resp.Content, msgs[1:], nil
	}
	return "", msgs[1:], fmt.Errorf("límite de pasos de agente alcanzado (%d)", maxSteps)
}
