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
	"syscall"
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
	"edit_file":   true,
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
		funcSpec("edit_file",
			"Reemplaza un texto exacto dentro de un archivo por otro. Útil para corregir bugs sin reescribir el archivo completo.",
			[]string{"path", "old", "new"},
			map[string]any{
				"path": strParam("ruta del archivo"),
				"old":  strParam("texto exacto a buscar (debe aparecer al menos una vez)"),
				"new":  strParam("texto de reemplazo"),
			}),
		funcSpec("tree",
			"Lista el árbol de directorios hasta cierta profundidad.",
			[]string{},
			map[string]any{
				"path":  strParam("directorio raíz (por defecto .)"),
				"depth": strParam("profundidad máxima, ej: 2 (por defecto 2)"),
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
	Old     string `json:"old"`
	New     string `json:"new"`
	Depth   string `json:"depth"`
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
	case "edit_file":
		return editFile(ctx, ws, a)
	case "tree":
		return tree(ctx, ws, a)
	}
	return "", fmt.Errorf("herramienta desconocida: %s", name)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n… (%d chars omitidos)", len(s)-n)
}

const (
	defaultCmdTimeout = 60 * time.Second
	maxCmdTimeout     = 600 * time.Second
)

// interactiveBlocked devuelve un mensaje si el comando colgaría la sesión
// (editor, pager, REPL, lectura de stdin o tail -f), o "" si es seguro.
func interactiveBlocked(cmd string) string {
	fields := strings.Fields(strings.TrimSpace(cmd))
	if len(fields) == 0 {
		return ""
	}
	name := strings.ToLower(fields[0])
	if p := filepath.Base(name); p != name {
		name = p
	}
	editors := map[string]string{
		"vim":     "usa `cat > archivo <<'EOF'` o la herramienta write_file",
		"vi":      "usa `cat > archivo <<'EOF'` o la herramienta write_file",
		"nano":    "escribe con write_file o `cat > archivo <<'EOF'`",
		"emacs":   "escribe con write_file",
		"less":    "usa `cat` para leer",
		"more":    "usa `cat` para leer",
		"top":     "usa `ps aux | head -20`",
		"htop":    "usa `ps aux | head -20`",
		"btop":    "usa `ps aux | head -20`",
		"watch":   "ejecutá el comando directo; sin watch (se queda repitiendo)",
		"ssh":     "no abras sesión interactiva; ejecutá el comando remoto de una vez",
		"telnet":  "no permitido",
		"mc":      "no permitido",
		"irb":     "usa `ruby script.rb`",
		"ipython": "usa `python3 script.py`",
	}
	if hint, ok := editors[name]; ok && len(fields) >= 1 {
		return fmt.Sprintf("%s (%s)", fields[0], hint)
	}
	if len(fields) == 1 {
		hint := map[string]string{
			"cat":     "cat sin argumentos lee de teclado y no termina; pasale un archivo p. ej. cat ruta.txt",
			"yes":     "yes no termina nunca",
			"python":  "ejecutá con `python -c '...'` o `python script.py`",
			"python3": "ejecutá con `python3 -c '...'` o `python3 script.py`",
			"node":    "ejecutá con `node -e '...'` o `node script.js`",
			"nodejs":  "ejecutá con `nodejs -e '...'` o `nodejs script.js`",
			"bun":     "ejecutá con `bun -e '...'` o `bun script.ts`",
			"deno":    "ejecutá con `deno run script.ts`",
			"fish":    "los comandos corren en sh; evitá fish interactivo",
			"sh":      "si querés correr un script: `sh script.sh`",
			"bash":    "si querés correr un script: `bash script.sh`",
			"zsh":     "si querés correr un script: `zsh script.sh`",
			"sqlite3": "pasá la consulta: `sqlite3 base.db 'SELECT 1;'`",
			"mysql":   "pasá la consulta con -e",
			"psql":    "pasá la consulta con -c",
		}[name]
		if hint != "" {
			return fmt.Sprintf("%s (%s)", fields[0], hint)
		}
	}
	if name == "tail" {
		for _, f := range fields[1:] {
			if f == "--follow" || (len(f) > 1 && f[0] == '-' && !strings.HasPrefix(f, "--") && (strings.ContainsRune(f, 'f') || strings.ContainsRune(f, 'F'))) {
				return "tail -f/-F mira el archivo sin terminar; usá `tail -n 20 ruta`"
			}
		}
	}
	return ""
}

func runCommand(ctx context.Context, ws *Workspace, a toolArgs) (string, error) {
	if strings.TrimSpace(a.Command) == "" {
		return "", fmt.Errorf("falta el comando")
	}
	if hint := interactiveBlocked(a.Command); hint != "" {
		return "", fmt.Errorf("no puedo ejecutar eso (cuelga la sesión): %s", hint)
	}
	if dir, ok := cdIfNeeded(a.Command); ok {
		if !filepath.IsAbs(dir) {
			dir = ws.Resolve(dir)
		}
		ws.SetCwd(dir)
		return "cwd: " + ws.Cwd(), nil
	}
	d := defaultCmdTimeout
	if a.Timeout != "" {
		if p, err := time.ParseDuration(a.Timeout); err == nil && p > 0 {
			d = p
		}
	}
	if d > maxCmdTimeout {
		d = maxCmdTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	cmd := exec.CommandContext(cctx, "sh", "-c", a.Command)
	cmd.Dir = ws.Cwd()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	s := truncate(string(out), 30000)
	if err != nil {
		if cctx.Err() != nil {
			return s, fmt.Errorf("se agotó el tiempo (%s máx). Si el comando era correcto, partilo en pasos más cortos", d)
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

func editFile(ctx context.Context, ws *Workspace, a toolArgs) (string, error) {
	if a.Path == "" || a.Old == "" {
		return "", fmt.Errorf("faltan path y old")
	}
	p := ws.Resolve(a.Path)
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("%s es un archivo binario", a.Path)
	}
	s := string(data)
	if !strings.Contains(s, a.Old) {
		return "", fmt.Errorf("no se encontró el texto a reemplazar en %s", a.Path)
	}
	n := strings.Count(s, a.Old)
	if err := os.WriteFile(p, []byte(strings.Replace(s, a.Old, a.New, -1)), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("editado %s: %d reemplazo(s)", a.Path, n), nil
}

func tree(ctx context.Context, ws *Workspace, a toolArgs) (string, error) {
	root := a.Path
	if root == "" {
		root = "."
	}
	root = ws.Resolve(root)
	depth := 2
	if a.Depth != "" {
		fmt.Sscanf(a.Depth, "%d", &depth)
		if depth < 1 {
			depth = 1
		}
	}
	var sb strings.Builder
	limit := 600
	skipDirs := map[string]bool{".git": true, "node_modules": true}
	var walk func(dir string, prefix string, d int) bool
	walk = func(dir, prefix string, d int) bool {
		if ctx.Err() != nil {
			return false
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return true
		}
		for i, e := range entries {
			if limit <= 0 {
				sb.WriteString(prefix + "└── … (límite alcanzado)\n")
				return false
			}
			last := i == len(entries)-1
			conn := "├── "
			nextPrefix := prefix + "│   "
			if last {
				conn = "└── "
				nextPrefix = prefix + "    "
			}
			name := e.Name()
			if e.IsDir() {
				if skipDirs[name] {
					continue
				}
				sb.WriteString(prefix + conn + name + "/\n")
				limit--
				if d < depth {
					if !walk(filepath.Join(dir, name), nextPrefix, d+1) {
						return false
					}
				}
			} else {
				sb.WriteString(prefix + conn + name + "\n")
				limit--
			}
		}
		return true
	}
	base := filepath.Base(root)
	if base == "." || base == "/" || base == "" {
		base = root
	}
	sb.WriteString(base + "/\n")
	walk(root, "", 1)
	return sb.String(), nil
}
