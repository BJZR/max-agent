package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sseChunks(parts ...map[string]any) string {
	var sb strings.Builder
	for _, p := range parts {
		data, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": p}}})
		fmt.Fprintf(&sb, "data: %s\n\n", data)
	}
	sb.WriteString("data: [DONE]\n\n")
	return sb.String()
}

func fakeLLM(t *testing.T, handler func(string) string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req chatReq
		json.Unmarshal(body, &req)
		last := ""
		if len(req.Messages) > 0 {
			last = req.Messages[len(req.Messages)-1].Role
		}
		if handler != nil {
			fmt.Fprint(w, handler(last))
			return
		}
		switch last {
		case "tool":
			fmt.Fprint(w, sseChunks(
				map[string]any{"role": "assistant", "content": "Listo, tarea completada."},
			))
		default:
			fmt.Fprint(w, sseChunks(
				map[string]any{"role": "assistant"},
				map[string]any{"tool_calls": []map[string]any{
					{"id": "call_1", "type": "function",
						"function": map[string]any{"name": "run_command", "arguments": `{"command":"echo hola","timeout":"5s"}`}},
				}},
			))
		}
	}))
}

func TestChatStream(t *testing.T) {
	srv := fakeLLM(t, func(last string) string {
		return sseChunks(
			map[string]any{"role": "assistant", "content": "ho"},
			map[string]any{"content": "la "},
			map[string]any{"content": "mundo"},
		)
	})
	defer srv.Close()
	p := NewProvider(Config{BaseURL: srv.URL, Model: "test", Tools: false, Temperature: 0.2})

	got, err := p.Chat(context.Background(), []Msg{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "hola mundo" {
		t.Fatalf("content = %q, want %q", got.Content, "hola mundo")
	}
	if len(got.ToolCalls) != 0 {
		t.Fatalf("no esperaba tool_calls, got %+v", got.ToolCalls)
	}
}

func TestChatStreamToolCalls(t *testing.T) {
	srv := fakeLLM(t, nil)
	defer srv.Close()
	p := NewProvider(Config{BaseURL: srv.URL, Model: "test", Tools: true, Temperature: 0.2})

	got, err := p.Chat(context.Background(), []Msg{{Role: "user", Content: "corre algo"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %d, want 1", len(got.ToolCalls))
	}
	tc := got.ToolCalls[0]
	if tc.Function.Name != "run_command" {
		t.Fatalf("name = %q", tc.Function.Name)
	}
	if !strings.Contains(tc.Function.Arguments, "echo hola") {
		t.Fatalf("arguments = %q", tc.Function.Arguments)
	}
}

func TestAgentLoop(t *testing.T) {
	srv := fakeLLM(t, nil)
	defer srv.Close()
	p := NewProvider(Config{BaseURL: srv.URL, Model: "test", Tools: true, Temperature: 0.2})

	appr := &alwaysApprove{}
	agent := &Agent{prov: p, config: Config{}, system: "test", ws: newWorkspace(), approver: appr}

	content, hist, err := agent.Chat(context.Background(), nil, "corre algo")
	if err != nil {
		t.Fatal(err)
	}
	if content != "Listo, tarea completada." {
		t.Fatalf("content = %q", content)
	}
	foundTool := false
	for _, m := range hist {
		if m.Role == "tool" {
			foundTool = true
			if m.ToolCallID == "" {
				t.Fatalf("tool msg sin ToolCallID")
			}
		}
	}
	if !foundTool {
		t.Fatalf("historial sin mensaje tool: %+v", hist)
	}
}

type alwaysApprove struct{}

func (a *alwaysApprove) Approve(name, args string) (bool, error) { return true, nil }

func TestExecTool(t *testing.T) {
	ws := newWorkspace()
	out, err := execTool(context.Background(), ws, "sh", "run_command", json.RawMessage(`{"command":"echo max_test","timeout":"5s"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "max_test" {
		t.Fatalf("out = %q", out)
	}
}

func TestWriteReadFile(t *testing.T) {
	ws := newWorkspace()
	dir := t.TempDir()
	path := dir + "/sub/nested.go"
	out, err := execTool(context.Background(), ws, "sh", "write_file", json.RawMessage(fmt.Sprintf(`{"path":%q,"content":"package p\n"}`, path)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "escrito") {
		t.Fatalf("out = %q", out)
	}
	out, err = execTool(context.Background(), ws, "sh", "read_file", json.RawMessage(fmt.Sprintf(`{"path":%q}`, path)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "package p" {
		t.Fatalf("read = %q", out)
	}
}

func TestSearchFiles(t *testing.T) {
	ws := newWorkspace()
	dir := t.TempDir()
	content := []byte("linea uno\nlinea bug aquí\n")
	if err := writePath(dir+"/a.go", content); err != nil {
		t.Fatal(err)
	}
	if err := writePath(dir+"/b.txt", []byte("sin coincidencia\n")); err != nil {
		t.Fatal(err)
	}
	out, err := searchFiles(context.Background(), ws, toolArgs{Path: dir, Pattern: "bug"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.go:2") || strings.Contains(out, "b.txt") {
		t.Fatalf("out = %q", out)
	}
}

func TestEditFile(t *testing.T) {
	ws := newWorkspace()
	dir := t.TempDir()
	path := dir + "/bug.go"
	if err := writePath(path, []byte("func a() { return 1 }\nfunc f() { return a() }\n")); err != nil {
		t.Fatal(err)
	}
	out, err := execTool(context.Background(), ws, "sh", "edit_file", json.RawMessage(
		fmt.Sprintf(`{"path":%q,"old":"return a()","new":"return a() + 1"}`, path)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1 reemplazo") {
		t.Fatalf("out = %q", out)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "return a() + 1") {
		t.Fatalf("no se aplicó el reemplazo: %q", data)
	}
	out, err = execTool(context.Background(), ws, "sh", "edit_file", json.RawMessage(
		fmt.Sprintf(`{"path":%q,"old":"no existe","new":"x"}`, path)))
	if err == nil {
		t.Fatalf("debería fallar si no encuentra old, out=%q", out)
	}
}

func TestTree(t *testing.T) {
	ws := newWorkspace()
	dir := t.TempDir()
	writePath(dir+"/a/x1.go", []byte(""))
	writePath(dir+"/a/s1/x2.go", []byte(""))
	writePath(dir+"/b.txt", []byte(""))
	out, err := execTool(context.Background(), ws, "sh", "tree", json.RawMessage(fmt.Sprintf(`{"path":%q,"depth":"2"}`, dir)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"a/", "b.txt", "x1.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tree = %q, falta %q", out, want)
		}
	}
	if strings.Contains(out, "x2.go") {
		t.Fatalf("depth 2 no debería listar el contenido de s1/: %q", out)
	}
}

func TestStoreRoundtrip(t *testing.T) {
	p := t.TempDir() + "/.max/sessions.json"
	st := store{"abc": sessionData{
		Updated:  time.Now(),
		Cwd:      "/tmp/foo",
		Messages: []Msg{{Role: "user", Content: "hola"}, {Role: "assistant", Content: "chau"}},
	}}
	if err := st.savePath(p); err != nil {
		t.Fatal(err)
	}
	got := loadStorePath(p)
	d, ok := got["abc"]
	if !ok {
		t.Fatal("sesión no recuperada")
	}
	if d.Cwd != "/tmp/foo" || len(d.Messages) != 2 || d.Messages[1].Role != "assistant" {
		t.Fatalf("datos corruptos: %+v", d)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("archivo no creado: %v", err)
	}
}

func TestStoreMissingFile(t *testing.T) {
	if s := loadStorePath(t.TempDir() + "/no-existe.json"); len(s) != 0 {
		t.Fatalf("esperaba store vacío, %v", s)
	}
}

func TestInteractiveBlockedClassify(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool // true = bloqueado
	}{
		{"vim holaa.txt", true},
		{"less log", true},
		{"top", true},
		{"htop", true},
		{"bash /tmp/x", false},
		{"bash", true},
		{"sh", true},
		{"python3", true},
		{"python3 -c 'print(1)'", false},
		{"python3 script.py", false},
		{"node", true},
		{"node -e 'console.log(1)'", false},
		{"cat", true},
		{"cat ruta.txt", false},
		{"cat -n ruta.txt", false},
		{"tail -f /var/log/x", true},
		{"tail -F /var/log/x", true},
		{"tail --follow /var/log/x", true},
		{"tail -n 5 ruta.txt", false},
		{"yes", true},
		{"yes | head -n 2", false},
	}
	for _, c := range cases {
		got := interactiveBlocked(c.cmd) != ""
		if got != c.want {
			t.Errorf("interactiveBlocked(%q) bloqueado=%v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestRunCommandRejectsInteractive(t *testing.T) {
	ws := newWorkspace()
	dir := t.TempDir()
	ws.SetCwd(dir)
	out, err := runCommand(context.Background(), ws, "sh", toolArgs{Command: "vim x.txt"})
	if err == nil || !strings.Contains(err.Error(), "cuelga la sesión") {
		t.Fatalf("esperaba rechazo de vim, got out=%q err=%v", out, err)
	}
}

func TestRunCommandTimeout(t *testing.T) {
	ws := newWorkspace()
	start := time.Now()
	_, err := runCommand(context.Background(), ws, "sh", toolArgs{Command: "sleep 5", Timeout: "300ms"})
	if err == nil || !strings.Contains(err.Error(), "se agotó el tiempo") {
		t.Fatalf("esperaba timeout, got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("el timeout tardó demasiado en cortar: %v", time.Since(start))
	}
}

func TestRunCommandBadTimeoutFallsBack(t *testing.T) {
	ws := newWorkspace()
	out, err := runCommand(context.Background(), ws, "sh", toolArgs{Command: "echo ok_fallback", Timeout: "noesduracion"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "ok_fallback" {
		t.Fatalf("out = %q", out)
	}
}

func TestMemoryStore(t *testing.T) {
	p := filepath.Join(t.TempDir(), "memory.md")
	m := loadMemoryPath(p)
	if m.List() != nil && len(m.List()) != 0 {
		t.Fatal("memoria debería estar vacía")
	}
	m.Add("el usuario trabaja en ftui")
	m.Add("el usuario trabaja en ftui")
	m.Add("el modelo es qwen7b")
	if got := m.List(); len(got) != 2 {
		t.Fatalf("esperaba 2 entradas, tengo %d: %v", len(got), got)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("archivo de memoria no creado: %v", err)
	}
	m2 := loadMemoryPath(p)
	if got := m2.List(); len(got) != 2 {
		t.Fatalf("recarga: %v", got)
	}
	blk := m2.Block()
	if !strings.HasPrefix(blk, "[MEMORIA") || !strings.Contains(blk, "ftui") {
		t.Fatalf("block = %q", blk)
	}
	m2.Clear()
	if got := m2.List(); len(got) != 0 {
		t.Fatalf("clear no vació: %v", got)
	}
}

func TestCondenseKeepsMemory(t *testing.T) {
	msgs := []Msg{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "[MEMORIA (persistente entre sesiones)]\n- hecho a\n- hecho b"},
		{Role: "user", Content: "[Contexto del entorno (dado por MAX)]\n..."},
		{Role: "user", Content: "mensaje viejo uno"},
		{Role: "user", Content: "mensaje viejo dos"},
		{Role: "assistant", Content: "respuesta final"},
	}
	out := condense(msgs, 40)
	if out[0].Role != "system" {
		t.Fatalf("head[0] = %+v", out[0])
	}
	if !strings.HasPrefix(out[1].Content, "[MEMORIA") || !strings.HasPrefix(out[2].Content, "[Contexto del entorno") {
		t.Fatalf("cabeza de memoria/entorno perdida: %q / %q", out[1].Content[:8], out[2].Content[:8])
	}
	last := out[len(out)-1]
	if last.Content != "respuesta final" {
		t.Fatalf("cola final = %q", last.Content)
	}
}

func TestAgentMemoryExtraction(t *testing.T) {
	calls := 0
	srv := fakeLLM(t, func(last string) string {
		calls++
		switch {
		case calls == 1:
			return sseChunks(map[string]any{"role": "assistant", "content": "Hago:\n\n```bash\necho tarea\n```"})
		case calls == 2:
			return sseChunks(map[string]any{"role": "assistant", "content": "Listo."})
		default:
			return sseChunks(map[string]any{"role": "assistant", "content": "- el usuario trabaja en /home/ftui\n- nada más"})
		}
	})
	defer srv.Close()
	p := NewProvider(Config{BaseURL: srv.URL, Model: "test", Tools: true, Temperature: 0.2})
	m := loadMemoryPath(filepath.Join(t.TempDir(), "memory.md"))
	agent := &Agent{prov: p, config: Config{Tools: true, Memory: true}, system: "test",
		ws: newWorkspace(), approver: &alwaysApprove{}, memory: m}
	if _, _, err := agent.Chat(context.Background(), nil, "haz algo"); err != nil {
		t.Fatal(err)
	}
	if calls < 3 {
		t.Fatalf("calls = %d, esperaba llamada de archivista (3)", calls)
	}
	list := m.List()
	if len(list) != 1 || !strings.Contains(list[0], "ftui") {
		t.Fatalf("memoria extraída inválida: %v", list)
	}
}

func TestWorkspaceCd(t *testing.T) {
	dir := t.TempDir()
	ws := newWorkspace()
	ws.SetCwd(dir)
	sub := dir + "/ia"
	out, err := runCommand(context.Background(), ws, "sh", toolArgs{Command: "mkdir -p ia && cd ia && pwd", Timeout: "5s"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != sub {
		t.Fatalf("pwd interno = %q, esperado %q", out, sub)
	}
	if ws.Cwd() != dir {
		t.Fatalf("cwd NO debe cambiar con cd interno: %q", ws.Cwd())
	}
	out, err = runCommand(context.Background(), ws, "sh", toolArgs{Command: "cd ia", Timeout: "5s"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "cwd:") {
		t.Fatalf("salida cd = %q", out)
	}
	if ws.Cwd() != sub {
		t.Fatalf("cwd tras cd = %q, esperado %q", ws.Cwd(), sub)
	}
	out, err = runCommand(context.Background(), ws, "sh", toolArgs{Command: "pwd", Timeout: "5s"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != sub {
		t.Fatalf("pwd persistido = %q, esperado %q", out, sub)
	}
}

func TestLoadConfigShellFilled(t *testing.T) {
	dir := t.TempDir()
	cfg, err := loadConfig(filepath.Join(dir, "no-existe.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Shell == "" {
		t.Fatal("shell vacío con archivo faltante: normalize() no se está aplicando")
	}
}

func TestRuntimeEnv(t *testing.T) {
	env := runtimeEnv()
	var path string
	found := false
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			path = strings.TrimPrefix(e, "PATH=")
			found = true
		}
	}
	if !found {
		t.Fatal("sin PATH en el entorno")
	}
	if !strings.Contains(path, "/usr/local/go/bin") {
		t.Fatalf("PATH no incluye /usr/local/go/bin: %q", path)
	}
	if !strings.Contains(path, "/usr/bin") {
		t.Fatalf("PATH no incluye /usr/bin: %q", path)
	}
}

func TestCdIfNeededStrict(t *testing.T) {
	dir := t.TempDir()
	ws := newWorkspace()
	ws.SetCwd(dir)
	if err := os.MkdirAll(dir+"/c", 0o755); err != nil {
		t.Fatal(err)
	}
	compound := "cd c\ntouch created_in_compound\nls"
	out, err := runCommand(context.Background(), ws, "sh", toolArgs{Command: compound, Timeout: "5s"})
	if err != nil {
		t.Fatal(err)
	}
	if ws.Cwd() != dir {
		t.Fatalf("cd compuesto multi-línea NO debe cambiar cwd: %q", ws.Cwd())
	}
	if !strings.Contains(out, "created_in_compound") {
		t.Fatalf("el comando compuesto no se ejecutó completo: %q", out)
	}
	if _, err := os.Stat(dir + "/c/created_in_compound"); err != nil {
		t.Fatalf("el archivo no se creó dentro de c/: %v", err)
	}
	if _, ok := cdIfNeeded("cd c && gcc x.c"); ok {
		t.Fatal("cd con && no debe tratarse como cd suelto")
	}
	if _, ok := cdIfNeeded("cd c\necho x"); ok {
		t.Fatal("cd multi-línea no debe tratarse como cd suelto")
	}
	if _, ok := cdIfNeeded("cd somewhere"); !ok {
		t.Fatal("cd simple debe reconocerse")
	}
}

func writePath(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func TestCondense(t *testing.T) {
	env := Msg{Role: "user", Content: "[Contexto del entorno (dado por MAX)]\na"}
	sys := Msg{Role: "system", Content: "sys"}
	fat := func(i int) Msg {
		return Msg{Role: "user", Content: strings.Repeat("x", 1000) + fmt.Sprintf(" %d", i)}
	}
	in := []Msg{sys, env, fat(1), fat(2), fat(3), fat(4)}
	out := condense(in, 3000)
	if out[0].Role != "system" || out[0].Content != "sys" {
		t.Fatalf("system debe preservarse al inicio")
	}
	if out[1].Role != "user" || !strings.HasPrefix(out[1].Content, "[Contexto del entorno") {
		t.Fatalf("contexto de entorno debe preservarse: %+v", out[:2])
	}
	if len(out) < 4 {
		t.Fatalf("la cola recortada quedó muy corta: %d", len(out))
	}
	last := out[len(out)-1]
	if !strings.Contains(last.Content, "4") {
		t.Fatalf("el último mensaje debe conservarse: %q", last.Content)
	}
	hasMarker := false
	for _, m := range out {
		if strings.Contains(m.Content, "se omitió") {
			hasMarker = true
		}
	}
	if !hasMarker {
		t.Fatal("debe aparecer el aviso de omisión")
	}
}

func TestAgentDedupFailedCmd(t *testing.T) {
	calls := 0
	fails := &failingCmd{cmd: "false"}
	srv := fakeLLM(t, func(last string) string {
		calls++
		switch calls {
		case 1, 2:
			return sseChunks(map[string]any{"role": "assistant",
				"content": "Primero:\n\n```bash\nfalse\n```"})
		}
		return sseChunks(map[string]any{"role": "assistant",
			"content": "Tienes razón, probaré otra cosa."})
	})
	defer srv.Close()
	p := NewProvider(Config{BaseURL: srv.URL, Model: "test", Tools: true, Temperature: 0.2})
	agent := &Agent{prov: p, config: Config{Tools: true}, system: "test", ws: newWorkspace(), approver: fails}

	content, _, err := agent.Chat(context.Background(), nil, "hola")
	if err != nil {
		t.Fatal(err)
	}
	if content == "" || content != "Tienes razón, probaré otra cosa." {
		t.Fatalf("content = %q", content)
	}
	if fails.runs != 1 {
		t.Fatalf("false se ejecutó %d veces, esperaba 1 (dedup)", fails.runs)
	}
}

type failingCmd struct {
	cmd  string
	runs int
}

func (f *failingCmd) Approve(name, args string) (bool, error) {
	if f.cmd != "" && strings.TrimSpace(args) == f.cmd {
		f.runs++
	}
	return true, nil
}

func TestResolve(t *testing.T) {
	ws := newWorkspace()
	ws.SetCwd("/a/b")
	if p := ws.Resolve("x"); p != "/a/b/x" {
		t.Fatalf("resolve relativa = %q", p)
	}
	if p := ws.Resolve("/tmp"); p != "/tmp" {
		t.Fatalf("resolve absoluta = %q", p)
	}
	if p := ws.Resolve(""); p != "/a/b" {
		t.Fatalf("resolve vacía = %q", p)
	}
}

func TestParseMaxLine(t *testing.T) {
	op := parseMaxLine(`web_search "hola mundo" max=3`)
	if op == nil || op.name != "web_search" {
		t.Fatalf("op = %+v", op)
	}
	if op.args["query"] != "hola mundo" || op.args["max"] != "3" {
		t.Fatalf("args = %+v", op.args)
	}
	op = parseMaxLine("git_log n=5")
	if op == nil || op.args["n"] != "5" {
		t.Fatalf("op = %+v", op)
	}
	op = parseMaxLine("git_status")
	if op == nil || len(op.args) != 0 {
		t.Fatalf("op = %+v", op)
	}
	op = parseMaxLine("hacer_algo x")
	if op != nil {
		t.Fatalf("esperaba nil, op = %+v", op)
	}
}

func TestMaxFenceRouting(t *testing.T) {
	body := "el cuerpo pagado ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	}))
	defer srv.Close()
	calls := 0
	llm := fakeLLM(t, func(last string) string {
		calls++
		if calls == 1 {
			return sseChunks(map[string]any{"role": "assistant", "content": "Voy a leer:\n\n```max\nhttp_get " + `"` + srv.URL + `/x` + `"` + "\n```"})
		}
		return sseChunks(map[string]any{"role": "assistant", "content": "Listo, ya está."})
	})
	defer llm.Close()
	p := NewProvider(Config{BaseURL: llm.URL, Model: "test", Tools: true, Temperature: 0.2})
	agent := &Agent{prov: p, config: Config{Tools: true}, system: "test", ws: newWorkspace(), approver: &alwaysApprove{}}
	content, hist, err := agent.Chat(context.Background(), nil, "leé esa página")
	if err != nil {
		t.Fatal(err)
	}
	if content != "Listo, ya está." {
		t.Fatalf("content = %q", content)
	}
	if calls < 2 {
		t.Fatalf("calls = %d", calls)
	}
	found := false
	for _, m := range hist {
		if strings.Contains(m.Content, "Resultado de las herramientas") && strings.Contains(m.Content, "el cuerpo pagado ok") {
			found = true
		}
	}
	if !found {
		t.Fatalf("falta resultado de herramienta en historial: %+v", hist)
	}
}

func TestRunCommandRedirectsMaxTool(t *testing.T) {
	ws := newWorkspace()
	out, err := runCommand(context.Background(), ws, "sh", toolArgs{Command: "web_search hola"})
	if err == nil {
		t.Fatalf("esperaba error, out = %q", out)
	}
	if !strings.Contains(err.Error(), "herramienta de MAX") {
		t.Fatalf("err = %v", err)
	}
}

func TestFencedCommands(t *testing.T) {
	s := "texto\n```bash\nls -la\n```\n```sh\necho hola\ncd /tmp\n```\n```c\nint x = 1;\n```\ny fin"
	got := fencedCommands(s)
	want := []string{"ls -la", "echo hola\ncd /tmp"}
	if len(got) != len(want) {
		t.Fatalf("got %q want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q want %q", got, want)
		}
	}
	if len(fencedCommands("sin bloques")) != 0 {
		t.Fatal("no debería haber comandos")
	}
}

func TestAppendFile(t *testing.T) {
	ws := newWorkspace()
	dir := t.TempDir()
	p := dir + "/log.txt"
	out, err := execTool(context.Background(), ws, "sh", "append_file", json.RawMessage(fmt.Sprintf(`{"path":%q,"content":"linea 1\n"}`, p)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "agregado a") {
		t.Fatalf("out = %q", out)
	}
	out, err = execTool(context.Background(), ws, "sh", "append_file", json.RawMessage(fmt.Sprintf(`{"path":%q,"content":"linea 2\n"}`, p)))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "linea 1\nlinea 2\n" {
		t.Fatalf("contenido = %q", string(data))
	}
}

func TestHttpGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "ok-body")
	}))
	defer srv.Close()
	out, err := httpGet(context.Background(), toolArgs{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ok-body") || !strings.Contains(out, "200") {
		t.Fatalf("out = %q", out)
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer errSrv.Close()
	if _, err := httpGet(context.Background(), toolArgs{URL: errSrv.URL}); err == nil {
		t.Fatal("esperaba error HTTP 404")
	}
}

func TestMemIntent(t *testing.T) {
	for _, in := range []string{"recordá que trabajo en /home/x", "guarda esto", "tené presente que uso fish", "prefiero go", "anota el puerto 8080"} {
		if !memIntent(in) {
			t.Fatalf("esperaba memIntent en %q", in)
		}
	}
	for _, in := range []string{"hola", "cuánto es 2+2?", "explica qué es un mutex", "haz un hello world"} {
		if memIntent(in) {
			t.Fatalf("no esperaba memIntent en %q", in)
		}
	}
}

func TestWebSearch(t *testing.T) {
	html := `<html><body>
<a rel="nofollow" class="result__a" href="https://example.com/duck/1">Título &amp; Uno</a>
<a class="result__snippet" href="https://example.com/s/1">Resumen del primer resultado</a>
<a rel="nofollow" class="result__a" href="https://example.com/duck/2">Segundo resultado</a>
<a class="result__snippet" href="https://example.com/s/2">Otro resumen aquí</a>
</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, html)
	}))
	defer srv.Close()
	orig := ddgEndpoint
	ddgEndpoint = srv.URL + "/?q="
	defer func() { ddgEndpoint = orig }()
	out, err := webSearch(context.Background(), toolArgs{Query: "max test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Título & Uno", "Segundo resultado", "example.com", "1. ", "Resumen del primer resultado"} {
		if !strings.Contains(out, want) {
			t.Fatalf("out = %q, falta %q", out, want)
		}
	}
}

func TestGitTools(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/a.txt", []byte("hola\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = runtimeEnv()
		return c.Run()
	}
	if err := run("init", "-q", "-b", "main"); err != nil {
		t.Skipf("git no disponible: %v", err)
	}
	run("config", "user.email", "t@t.io")
	run("config", "user.name", "test")
	run("add", "a.txt")
	if err := run("commit", "-q", "-m", "Oneline inicial"); err != nil {
		t.Fatal(err)
	}
	ws := newWorkspace()
	ws.SetCwd(dir)
	out, err := gitStatus(context.Background(), ws, toolArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "main") {
		t.Fatalf("status = %q", out)
	}
	out, err = gitLog(context.Background(), ws, toolArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Oneline inicial") {
		t.Fatalf("log = %q", out)
	}
	out, err = gitBranch(context.Background(), ws, toolArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "main" {
		t.Fatalf("branch = %q", out)
	}
}

func TestEnvSnapshot(t *testing.T) {
	s := envSnapshot()
	if s == "" {
		t.Fatal("envSnapshot vacío")
	}
	if !strings.Contains(s, "directorio de trabajo") {
		t.Fatalf("falta cwd en snapshot: %q", s[:80])
	}
}

func TestLooksLikeTask(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"hazme un hello world", true},
		{"busca un bug", true},
		{"instala gcc", true},
		{"cuál es el significado de la vida", false},
		{"explica qué es un Mutex", false},
		{"haz build de este archivo", true},
	}
	for _, c := range cases {
		if got := looksLikeTask(c.in); got != c.want {
			t.Errorf("looksLikeTask(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestAgentNudge(t *testing.T) {
	calls := 0
	srv := fakeLLM(t, func(last string) string {
		calls++
		return sseChunks(map[string]any{
			"role":    "assistant",
			"content": "Primero debo crear el archivo. Aquí tienes los pasos...",
		})
	})
	defer srv.Close()
	p := NewProvider(Config{BaseURL: srv.URL, Model: "test", Tools: true, Temperature: 0.2})
	agent := &Agent{prov: p, config: Config{Tools: true}, system: "test", ws: newWorkspace(), approver: &alwaysApprove{}}
	content, hist, err := agent.Chat(context.Background(), nil, "haz un hello world en C")
	if err != nil {
		t.Fatal(err)
	}
	if calls < 2 {
		t.Fatalf("calls = %d, esperaba al menos un nudge", calls)
	}
	if content == "" {
		t.Fatal("sin respuesta final")
	}
	for _, m := range hist {
		if m.Content == nudgeMsg {
			t.Fatal("el nudge no debería quedar en el historial devuelto")
		}
	}
}

func TestAgentFenceFallback(t *testing.T) {
	calls := 0
	srv := fakeLLM(t, func(last string) string {
		calls++
		if calls == 1 {
			return sseChunks(map[string]any{"role": "assistant",
				"content": "Hago un hello world:\n\n```bash\necho hello_max_fence\n```"})
		}
		return sseChunks(map[string]any{"role": "assistant", "content": "Listo, tarea completada."})
	})
	defer srv.Close()
	p := NewProvider(Config{BaseURL: srv.URL, Model: "test", Tools: true, Temperature: 0.2})
	agent := &Agent{prov: p, config: Config{Tools: true}, system: "test", ws: newWorkspace(), approver: &alwaysApprove{}}

	content, hist, err := agent.Chat(context.Background(), nil, "haz un hello world")
	if err != nil {
		t.Fatal(err)
	}
	if content != "Listo, tarea completada." {
		t.Fatalf("content = %q", content)
	}
	if calls < 2 {
		t.Fatalf("calls = %d, esperaba que el loop continuara tras ejecutar la fence", calls)
	}
	found := false
	for _, m := range hist {
		if strings.Contains(m.Content, "Resultado de las herramientas") && strings.Contains(m.Content, "hello_max_fence") {
			found = true
		}
	}
	if !found {
		t.Fatalf("falta el resultado del comando en el historial: %+v", hist)
	}
}

func TestAgentNoNudgeAfterExecute(t *testing.T) {
	calls := 0
	srv := fakeLLM(t, func(last string) string {
		calls++
		if calls == 1 {
			return sseChunks(map[string]any{"role": "assistant",
				"content": "Ejecuto:\n\n```bash\necho hice_algo\n```"})
		}
		return sseChunks(map[string]any{"role": "assistant", "content": "Listo, ya está hecho."})
	})
	defer srv.Close()
	p := NewProvider(Config{BaseURL: srv.URL, Model: "test", Tools: true, Temperature: 0.2})
	agent := &Agent{prov: p, config: Config{Tools: true}, system: "test", ws: newWorkspace(), approver: &alwaysApprove{}}

	content, hist, err := agent.Chat(context.Background(), nil, "haz un hello world")
	if err != nil {
		t.Fatal(err)
	}
	if content != "Listo, ya está hecho." {
		t.Fatalf("content = %q", content)
	}
	for _, m := range hist {
		if m.Content == nudgeMsg {
			t.Fatal("no debería nudgar si ya ejecutó algo")
		}
	}
}
