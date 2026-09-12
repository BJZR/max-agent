package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type toolOp struct {
	kind string // "cmd" o "tool"
	text string
	name string
	args map[string]any
}

var fenceRe = regexp.MustCompile("(?s)```([A-Za-z0-9]*)[ \t]*\n(.*?)```")
var bashLangs = map[string]bool{"bash": true, "sh": true, "shell": true, "zsh": true, "fish": true}
var maxLangs = map[string]bool{"max": true, "tools": true, "tool": true}
var maxPrimary = map[string]string{
	"web_search": "query", "http_get": "url",
	"git_status": "path", "git_branch": "path", "git_log": "path", "git_diff": "path",
}
var maxToolNames = map[string]bool{"web_search": true, "http_get": true, "git_status": true, "git_branch": true, "git_log": true, "git_diff": true}

func fenceOps(s string) []toolOp {
	var out []toolOp
	for _, m := range fenceRe.FindAllStringSubmatch(s, -1) {
		lang := strings.ToLower(strings.TrimSpace(m[1]))
		body := strings.TrimSpace(m[2])
		if body == "" {
			continue
		}
		switch {
		case lang == "" || bashLangs[lang]:
			out = append(out, toolOp{kind: "cmd", text: body})
		case maxLangs[lang]:
			for _, line := range strings.Split(body, "\n") {
				if to := parseMaxLine(line); to != nil {
					out = append(out, *to)
				}
			}
		}
	}
	return out
}

func fencedCommands(s string) []string {
	var out []string
	for _, op := range fenceOps(s) {
		if op.kind == "cmd" {
			out = append(out, op.text)
		}
	}
	return out
}

func parseMaxLine(line string) *toolOp {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	fields := strings.SplitN(line, " ", 2)
	name := strings.TrimSpace(fields[0])
	if _, ok := maxPrimary[name]; !ok {
		return nil
	}
	to := &toolOp{kind: "tool", name: name, args: map[string]any{}}
	if len(fields) == 1 {
		return to
	}
	rest := strings.TrimSpace(fields[1])
	var toks []string
	var cur strings.Builder
	inQ := false
	for _, r := range rest {
		switch {
		case r == '"':
			inQ = !inQ
		case r == ' ' || r == '\t':
			if inQ {
				cur.WriteRune(r)
			} else if cur.Len() > 0 {
				toks = append(toks, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		toks = append(toks, cur.String())
	}
	primary := maxPrimary[name]
	for _, t := range toks {
		if k, v, ok := strings.Cut(t, "="); ok {
			if k == "max" {
				if n, err := strconv.Atoi(v); err == nil {
					to.args["max"] = strconv.Itoa(n)
				}
			} else if k == "n" {
				if n, err := strconv.Atoi(v); err == nil {
					to.args["n"] = strconv.Itoa(n)
				}
			} else {
				to.args[k] = v
			}
			continue
		}
		if _, ok := to.args[primary]; !ok {
			to.args[primary] = t
		}
	}
	return to
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
	memory      *Memory
	onToken     func(string)
	onReasoning func(string)
	onTool      func(name, args string)
	onToolOut   func(string)
}

func (a *Agent) maxSteps() int {
	if a.config.MaxSteps > 0 {
		return a.config.MaxSteps
	}
	return 12
}

func (a *Agent) ctxBudget() int {
	if a.config.CtxChars > 0 {
		return a.config.CtxChars
	}
	return 16000
}

var taskWords = []string{
	"haz", "hace", "hacer", "crea", "crear", "genera", "escribe", "escríbeme", "implementa",
	"instala", "arregla", "corrige", "corrigeme", "repara", "busca", "analiza", "investiga",
	"compila", "ejecuta", "corre", "prueba", "muestra", "verifica", "modifica", "actualiza",
	"instalar", "construye", "parsea", "descarga", "extrae", "ordena",
}

var memWords = []string{
	"recuerda", "recorda", "recordá", "guarda", "memoriza", "memoria", "acordate", "acordáte", "anota",
	"no olvides", "tené presente", "ten presente", "de preferencia", "prefiero",
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

func memIntent(s string) bool {
	low := strings.ToLower(s)
	for _, w := range memWords {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

const nudgeMsg = "No ejecutaste nada todavía. Si la petición toca la máquina, hazlo AHORA: envía los comandos en un bloque ```bash (primero explora con ls, cat, grep -n, rg; verifica antes de actuar). No repitas la explicación: ejecuta."

func (a *Agent) Chat(ctx context.Context, history []Msg, input string) (string, []Msg, error) {
	steps := a.maxSteps()
	budget := a.ctxBudget()
	msgs := make([]Msg, 0, len(history)+steps+2)
	msgs = append(msgs, Msg{Role: "system", Content: a.system})
	if a.memory != nil && a.config.Memory {
		if blk := a.memory.Block(); blk != "" {
			msgs = append(msgs, Msg{Role: "user", Content: blk})
		}
	}
	msgs = append(msgs, condense(history, budget)...)
	msgs = append(msgs, Msg{Role: "user", Content: input})

	nudged := false
	anythingExecuted := false
	attempted := map[string]bool{}
	for step := 0; step < steps; step++ {
		if totalChars(msgs) > budget {
			msgs = condense(msgs, budget)
		}
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
				out, err := execTool(ctx, a.ws, a.config.Shell, name, json.RawMessage(args))
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
			if ops := fenceOps(resp.Content); len(ops) > 0 {
				var results strings.Builder
				executed := false
				failed := false
				for _, op := range ops {
					key := op.text
					if op.kind == "tool" {
						key = op.name + " " + op.text
					}
					if attempted[key] {
						results.WriteString("$ " + key + "\n(BLOQUEADO: esto ya falló y no se re-ejecuta. Estás en " + a.ws.Cwd() + ". Probá una alternativa NUEVA.)\n")
						failed = true
						executed = true
						continue
					}
					attempted[key] = true
					name := "run_command"
					disp := op.text
					var raw json.RawMessage
					if op.kind == "tool" {
						name = op.name
						b, _ := json.Marshal(map[string]any(op.args))
						raw = b
					} else {
						b, _ := json.Marshal(map[string]any{"command": op.text, "timeout": "120s"})
						raw = b
					}
					if a.onTool != nil {
						a.onTool(name, disp)
					}
					if a.approver != nil {
						approved, err := a.approver.Approve(name, disp)
						if err != nil {
							return "", msgs[1:], err
						}
						if !approved {
							results.WriteString("$ " + key + "\n(usuario rechazó)\n")
							continue
						}
					}
					out, err := execTool(ctx, a.ws, a.config.Shell, name, raw)
					if a.onToolOut != nil {
						a.onToolOut(out)
					}
					if err != nil {
						out = "error: " + err.Error() + "\n" + out
						failed = true
					}
					results.WriteString("$ " + key + "\n" + out + "\n")
					executed = true
					anythingExecuted = true
				}
				if executed {
					msg := "Resultado de las herramientas que ejecutaste:\n" + strings.TrimSpace(results.String())
					if failed {
						msg += "\n\nAl menos una herramienta falló. Estás en " + a.ws.Cwd() + ". No repitas lo que falló: empezá por el PRIMER error. Comandos CORTOS, de a uno. Reintentá con algo distinto."
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
		if a.config.Memory && a.memory != nil && (anythingExecuted || looksLikeTask(input) || memIntent(input)) {
			memMsgs := msgs
			go func() {
				ectx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				a.extractMemory(ectx, memMsgs)
			}()
		}
		return resp.Content, filterNudge(msgs[1:]), nil
	}
	return "", filterNudge(msgs[1:]), fmt.Errorf("límite de pasos de agente alcanzado (%d)", steps)
}

const memArchivistSystem = "Sos el archivista de memoria de MAX, un agente minimalista. Te pasan una conversación reciente del asistente. Si contiene 1-3 hechos estables y durables que el agente deba recordar SIEMPRE, en cualquier sesión futura (preferencias del usuario, ubicación de proyectos, decisiones técnicas, versión de herramientas, comandos o trucos que funcionan), devolvé SOLO una línea por hecho usando el formato '- hecho'. Si no hay nada durable que valga la pena recordar, devolvé exactamente la palabra NADA. No repitas instrucciones ni conversación: solo hechos durables."

func (a *Agent) extractMemory(ctx context.Context, msgs []Msg) {
	var b strings.Builder
	b.WriteString("### Conversación reciente ###\n")
	start := 0
	if len(msgs) > 12 {
		start = len(msgs) - 12
	}
	total := 0
	for i := start; i < len(msgs) && total < 5000; i++ {
		m := msgs[i]
		line := m.Role + ": " + m.Content + "\n"
		if total+len(line) > 5000 {
			line = line[:5000-total]
		}
		b.WriteString(line)
		total += len(line)
	}
	memMsgs := []Msg{
		{Role: "system", Content: memArchivistSystem},
		{Role: "user", Content: b.String()},
	}
	ectx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := a.prov.Chat(ectx, memMsgs, nil, nil)
	if err != nil {
		return
	}
	for _, line := range strings.Split(resp.Content, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		low := strings.ToLower(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(low, "nada") || strings.EqualFold(line, "NADA") ||
			strings.HasPrefix(low, "no hay") {
			continue
		}
		a.memory.Add(line)
	}
}

func filterNudge(msgs []Msg) []Msg {
	out := make([]Msg, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "user" && (m.Content == nudgeMsg || strings.HasPrefix(m.Content, memBlockPrefix)) {
			continue
		}
		out = append(out, m)
	}
	return out
}

const ctxCharsBudget = 16000

func totalChars(msgs []Msg) int {
	var n int
	for _, m := range msgs {
		n += len(m.Content) + len(m.ToolCallID) + len(m.Name)
	}
	return n
}

func condense(msgs []Msg, budget int) []Msg {
	if len(msgs) == 0 {
		return msgs
	}
	head := []Msg{msgs[0]}
	i := 1
	isHead := func(m Msg) bool {
		return m.Role == "user" && (strings.HasPrefix(m.Content, memBlockPrefix) ||
			strings.HasPrefix(m.Content, "[Contexto del entorno"))
	}
	for i < len(msgs) && isHead(msgs[i]) {
		head = append(head, msgs[i])
		i++
	}
	total := 0
	for _, m := range head {
		total += len(m.Content)
	}
	var tail []Msg
	for j := len(msgs) - 1; j >= i; j-- {
		m := msgs[j]
		if total+len(m.Content) > budget && len(tail) >= 2 {
			break
		}
		total += len(m.Content)
		tail = append([]Msg{m}, tail...)
	}
	out := make([]Msg, 0, len(msgs))
	out = append(out, head...)
	if i+len(tail) < len(msgs) {
		out = append(out, Msg{Role: "user",
			Content: "Parte de la conversación anterior se omitió por el límite de contexto. Continuá con la tarea usando lo último que se dijo."})
	}
	out = append(out, tail...)
	return out
}
