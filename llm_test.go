package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
	out, err := execTool(context.Background(), ws, "run_command", json.RawMessage(`{"command":"echo max_test","timeout":"5s"}`))
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
	out, err := execTool(context.Background(), ws, "write_file", json.RawMessage(fmt.Sprintf(`{"path":%q,"content":"package p\n"}`, path)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "escrito") {
		t.Fatalf("out = %q", out)
	}
	out, err = execTool(context.Background(), ws, "read_file", json.RawMessage(fmt.Sprintf(`{"path":%q}`, path)))
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
	out, err := execTool(context.Background(), ws, "edit_file", json.RawMessage(
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
	out, err = execTool(context.Background(), ws, "edit_file", json.RawMessage(
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
	out, err := execTool(context.Background(), ws, "tree", json.RawMessage(fmt.Sprintf(`{"path":%q,"depth":"2"}`, dir)))
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

func TestWorkspaceCd(t *testing.T) {
	dir := t.TempDir()
	ws := newWorkspace()
	ws.SetCwd(dir)
	sub := dir + "/ia"
	out, err := runCommand(context.Background(), ws, toolArgs{Command: "mkdir -p ia && cd ia && pwd", Timeout: "5s"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != sub {
		t.Fatalf("pwd interno = %q, esperado %q", out, sub)
	}
	if ws.Cwd() != dir {
		t.Fatalf("cwd NO debe cambiar con cd interno: %q", ws.Cwd())
	}
	out, err = runCommand(context.Background(), ws, toolArgs{Command: "cd ia", Timeout: "5s"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "cwd:") {
		t.Fatalf("salida cd = %q", out)
	}
	if ws.Cwd() != sub {
		t.Fatalf("cwd tras cd = %q, esperado %q", ws.Cwd(), sub)
	}
	out, err = runCommand(context.Background(), ws, toolArgs{Command: "pwd", Timeout: "5s"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != sub {
		t.Fatalf("pwd persistido = %q, esperado %q", out, sub)
	}
}

func writePath(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
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
		if strings.Contains(m.Content, "Resultado de los comandos") && strings.Contains(m.Content, "hello_max_fence") {
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
