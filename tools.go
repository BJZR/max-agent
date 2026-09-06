package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type toolSpec struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

var toolSpecs []toolSpec

var dangerous = map[string]bool{
	"run_command": true,
	"write_file":  true,
}

func funcSpec(name, desc string, required []string, props map[string]any) toolSpec {
	return toolSpec{
		Type: "function",
		Function: toolFunction{
			Name:        name,
			Description: desc,
			Parameters: map[string]any{
				"type":                 "object",
				"properties":           props,
				"additionalProperties": false,
				"required":             required,
			},
		},
	}
}

func strParam(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func init() {
	toolSpecs = []toolSpec{
		funcSpec("run_command",
			"Ejecuta un comando de shell (sh -c) y devuelve su salida. Úsalo para buils, tests, git e inspeccionar el sistema.",
			[]string{"command"},
			map[string]any{
				"command": strParam("comando a ejecutar, ej: go test ./..."),
				"timeout": strParam("duración máxima, ej: 30s (opcional)"),
			}),
		funcSpec("read_file",
			"Lee un archivo de texto y devuelve su contenido. Permite rango de líneas opcional.",
			[]string{"path"},
			map[string]any{
				"path":   strParam("ruta del archivo"),
				"offset": strParam("línea inicial (opcional)"),
				"limit":  strParam("cantidad de líneas (opcional)"),
			}),
		funcSpec("write_file",
			"Escribe un archivo con el contenido dado (reemplaza si existe). Crea directorios si faltan.",
			[]string{"path", "content"},
			map[string]any{
				"path":    strParam("ruta del archivo"),
				"content": strParam("contenido completo a escribir"),
			}),
		funcSpec("list_dir",
			"Lista las entradas de un directorio.",
			[]string{"path"},
			map[string]any{"path": strParam("ruta del directorio (por defecto .)")}),
		funcSpec("search_files",
			"Busca texto dentro de archivos del proyecto (grep).",
			[]string{"pattern", "path"},
			map[string]any{
				"pattern": strParam("patrón de búsqueda"),
				"path":    strParam("directorio raíz (por defecto .)"),
				"include": strParam("filtro de archivo, ej: *.go (opcional)"),
			}),
	}
}

type toolArgs struct {
	Command string `json:"command"`
	Timeout string `json:"timeout"`
	Path    string `json:"path"`
	Offset  string `json:"offset"`
	Limit   string `json:"limit"`
	Content string `json:"content"`
	Pattern string `json:"pattern"`
	Include string `json:"include"`
}

func execTool(ctx context.Context, ws *Workspace, name string, raw json.RawMessage) (string, error) {
	var a toolArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("argumentos inválidos: %v", err)
	}
	switch name {
	case "run_command":
		return runCommand(ctx, ws, a)
	case "read_file":
		return readFile(ctx, ws, a)
	case "write_file":
		return writeFile(ctx, ws, a)
	case "list_dir":
		return listDir(ctx, ws, a)
	case "search_files":
		return searchFiles(ctx, ws, a)
	}
	return "", fmt.Errorf("herramienta desconocida: %s", name)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n… (%d chars omitidos)", len(s)-n)
}

func runCommand(ctx context.Context, ws *Workspace, a toolArgs) (string, error) {
	if strings.TrimSpace(a.Command) == "" {
		return "", fmt.Errorf("falta el comando")
	}
	if dir, ok := cdIfNeeded(a.Command); ok {
		if !filepath.IsAbs(dir) {
			dir = ws.Resolve(dir)
		}
		ws.SetCwd(dir)
		return "cwd: " + ws.Cwd(), nil
	}
	cctx := ctx
	if a.Timeout != "" {
		d, err := time.ParseDuration(a.Timeout)
		if err != nil {
			return "", fmt.Errorf("timeout inválido: %v", err)
		}
		var cancel context.CancelFunc
		cctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	cmd := exec.CommandContext(cctx, "sh", "-c", a.Command)
	cmd.Dir = ws.Cwd()
	out, err := cmd.CombinedOutput()
	s := truncate(string(out), 30000)
	if err != nil {
		if cctx.Err() != nil {
			return s, fmt.Errorf("timeout: %v", cctx.Err())
		}
		return s, fmt.Errorf("salida: %v", err)
	}
	return s, nil
}

func readFile(ctx context.Context, ws *Workspace, a toolArgs) (string, error) {
	if a.Path == "" {
		return "", fmt.Errorf("falta la ruta")
	}
	p := ws.Resolve(a.Path)
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("%s es un archivo binario", a.Path)
	}
	offset, limit := 0, 0
	if a.Offset != "" {
		fmt.Sscanf(a.Offset, "%d", &offset)
	}
	if a.Limit != "" {
		fmt.Sscanf(a.Limit, "%d", &limit)
	}
	var sb strings.Builder
	lines := bytes.Split(data, []byte{'\n'})
	if offset > len(lines) {
		return "", fmt.Errorf("offset %d fuera de rango (%d líneas)", offset, len(lines))
	}
	end := len(lines)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	for _, l := range lines[offset:end] {
		sb.Write(l)
		sb.WriteByte('\n')
	}
	return truncate(sb.String(), 60000), nil
}

func writeFile(ctx context.Context, ws *Workspace, a toolArgs) (string, error) {
	if a.Path == "" {
		return "", fmt.Errorf("falta la ruta")
	}
	p := ws.Resolve(a.Path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(a.Content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("escrito %s (%d bytes)", a.Path, len(a.Content)), nil
}

func listDir(ctx context.Context, ws *Workspace, a toolArgs) (string, error) {
	path := a.Path
	if path == "" {
		path = "."
	}
	path = ws.Resolve(path)
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	var lines []string
	for i, e := range entries {
		if i >= 300 {
			lines = append(lines, fmt.Sprintf("… (%d más)", len(entries)-i))
			break
		}
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		lines = append(lines, name)
	}
	return strings.Join(lines, "\n"), nil
}

func searchFiles(ctx context.Context, ws *Workspace, a toolArgs) (string, error) {
	root := a.Path
	if root == "" {
		root = "."
	}
	root = ws.Resolve(root)
	pattern := a.Pattern
	if pattern == "" {
		return "", fmt.Errorf("falta el patrón")
	}
	var lines []string
	count := 0
	skipDirs := map[string]bool{".git": true, "node_modules": true}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if a.Include != "" {
			if ok, _ := filepath.Match(a.Include, d.Name()); !ok {
				return nil
			}
		}
		info, err := d.Info()
		if err != nil || info.Size() > 2<<20 {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		for i, l := range bytes.Split(data, []byte{'\n'}) {
			if count >= 200 {
				return context.Canceled
			}
			if bytes.Contains(l, []byte(pattern)) {
				lines = append(lines, fmt.Sprintf("%s:%d: %s", p, i+1, truncate(string(l), 300)))
				count++
			}
		}
		return nil
	})
	if err != nil && err != context.Canceled {
		return strings.Join(lines, "\n"), err
	}
	return strings.Join(lines, "\n"), nil
}
