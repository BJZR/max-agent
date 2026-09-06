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
	agent := &Agent{prov: p, config: Config{}, system: "test", approver: appr}

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
	out, err := execTool(context.Background(), "run_command", json.RawMessage(`{"command":"echo max_test","timeout":"5s"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "max_test" {
		t.Fatalf("out = %q", out)
	}
}

func TestWriteReadFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sub/nested.go"
	out, err := execTool(context.Background(), "write_file", json.RawMessage(fmt.Sprintf(`{"path":%q,"content":"package p\n"}`, path)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "escrito") {
		t.Fatalf("out = %q", out)
	}
	out, err = execTool(context.Background(), "read_file", json.RawMessage(fmt.Sprintf(`{"path":%q}`, path)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "package p" {
		t.Fatalf("read = %q", out)
	}
}

func TestSearchFiles(t *testing.T) {
	dir := t.TempDir()
	content := []byte("linea uno\nlinea bug aquí\n")
	if err := writePath(dir+"/a.go", content); err != nil {
		t.Fatal(err)
	}
	if err := writePath(dir+"/b.txt", []byte("sin coincidencia\n")); err != nil {
		t.Fatal(err)
	}
	out, err := searchFiles(context.Background(), toolArgs{Path: dir, Pattern: "bug"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.go:2") || strings.Contains(out, "b.txt") {
		t.Fatalf("out = %q", out)
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
	agent := &Agent{prov: p, config: Config{Tools: true}, system: "test", approver: &alwaysApprove{}}

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
