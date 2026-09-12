package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	"append_file": true,
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
		funcSpec("append_file",
			"Agrega contenido al final de un archivo. Si no existe, lo crea.",
			[]string{"path", "content"},
			map[string]any{
				"path":    strParam("ruta del archivo"),
				"content": strParam("contenido a agregar al final"),
			}),
		funcSpec("http_get",
			"Descarga el contenido de una URL (GET) y devuelve el cuerpo truncado. Útil para leer docs, APIs y páginas sin usar el shell.",
			[]string{"url"},
			map[string]any{
				"url":     strParam("URL completa, ej: https://example.com/api"),
				"timeout": strParam("duración máxima, ej: 10s (opcional)"),
			}),
		funcSpec("web_search",
			"Busca en internet y devuelve los mejores resultados (título, resumen y URL). Usalo para información actual que no sabés; después leé la página con http_get.",
			[]string{"query"},
			map[string]any{
				"query": strParam("consulta de búsqueda"),
				"max":   strParam("cantidad de resultados (1-10, por defecto 5)"),
			}),
		funcSpec("git_status",
			"Estado del repositorio git actual (rama y archivos modificados). Read-only.",
			[]string{},
			map[string]any{
				"path": strParam("directorio del repo (por defecto el actual)"),
			}),
		funcSpec("git_branch",
			"Rama actual del repositorio git. Read-only.",
			[]string{},
			map[string]any{
				"path": strParam("directorio del repo (por defecto el actual)"),
			}),
		funcSpec("git_log",
			"Últimos commits del repositorio git (uno por línea). Read-only.",
			[]string{},
			map[string]any{
				"path": strParam("directorio del repo (por defecto el actual)"),
				"n":    strParam("cantidad de commits (por defecto 10)"),
			}),
		funcSpec("git_diff",
			"Diferencias sin commitear del repositorio git. Read-only.",
			[]string{},
			map[string]any{
				"path": strParam("directorio del repo (por defecto el actual)"),
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
	URL     string `json:"url"`
	Query   string `json:"query"`
	Max     string `json:"max"`
	N       string `json:"n"`
	Result  int    `json:"result"`
}

func execTool(ctx context.Context, ws *Workspace, shell, name string, raw json.RawMessage) (string, error) {
	var a toolArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("argumentos inválidos: %v", err)
	}
	switch name {
	case "run_command":
		return runCommand(ctx, ws, shell, a)
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
	case "append_file":
		return appendFile(ctx, ws, a)
	case "http_get":
		return httpGet(ctx, a)
	case "web_search":
		return webSearch(ctx, a)
	case "git_status":
		return gitStatus(ctx, ws, a)
	case "git_branch":
		return gitBranch(ctx, ws, a)
	case "git_log":
		return gitLog(ctx, ws, a)
	case "git_diff":
		return gitDiff(ctx, ws, a)
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

func runCommand(ctx context.Context, ws *Workspace, shell string, a toolArgs) (string, error) {
	if strings.TrimSpace(a.Command) == "" {
		return "", fmt.Errorf("falta el comando")
	}
	if words := strings.Fields(a.Command); len(words) > 0 && maxToolNames[words[0]] {
		return "", fmt.Errorf("%s es una herramienta de MAX, no un comando de la terminal. Invocala en un bloque con tres acentos y la palabra max, por ejemplo: \n```max\n%s ...\n```", words[0], words[0])
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
	if shell == "" {
		shell = "/bin/sh"
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
	cmd := exec.CommandContext(cctx, shell, "-c", a.Command)
	cmd.Dir = ws.Cwd()
	cmd.Env = runtimeEnv()
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

// runtimeEnv devuelve el entorno del proceso con un PATH enriquecido con los
// directorios de binarios más comunes (Go, GOPATH, estándares), para que
// herramientas como `go` funcionen aunque el servicio no herede ese PATH.
func runtimeEnv() []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	home, err := os.UserHomeDir()
	dirs := []string{"/usr/local/go/bin"}
	if err == nil && home != "" {
		dirs = append(dirs, home+"/go/bin")
	}
	dirs = append(dirs, "/usr/local/bin", "/usr/bin", "/bin")
	var extra []string
	for _, d := range dirs {
		if d != "" && !strings.Contains(path, d) {
			extra = append(extra, d)
		}
	}
	if len(extra) > 0 {
		path = strings.Join(extra, ":") + ":" + path
	}
	env := os.Environ()
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = "PATH=" + path
			return env
		}
	}
	return append(env, "PATH="+path)
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
	if offset < 0 || limit < 0 {
		return "", fmt.Errorf("offset y limit deben ser >= 0")
	}
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

func appendFile(ctx context.Context, ws *Workspace, a toolArgs) (string, error) {
	if a.Path == "" {
		return "", fmt.Errorf("falta la ruta")
	}
	p := ws.Resolve(a.Path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	n, err := f.WriteString(a.Content)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("agregado a %s (%d bytes)", a.Path, n), nil
}

func httpGet(ctx context.Context, a toolArgs) (string, error) {
	if a.URL == "" {
		return "", fmt.Errorf("falta la url")
	}
	d := 15 * time.Second
	if a.Timeout != "" {
		if td, err := time.ParseDuration(a.Timeout); err == nil && td > 0 {
			d = td
		}
	}
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "MAX-agent/1.0")
	client := &http.Client{Timeout: d}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 20000))
	if err != nil {
		return "", err
	}
	s := fmt.Sprintf("[%s %d] %s", resp.Header.Get("Content-Type"), resp.StatusCode, truncate(string(body), 20000))
	if resp.StatusCode >= 400 {
		return s, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return s, nil
}

var ddgEndpoint = "https://html.duckduckgo.com/html/?q="

func webSearch(ctx context.Context, a toolArgs) (string, error) {
	q := strings.TrimSpace(a.Query)
	if q == "" {
		return "", fmt.Errorf("falta la consulta")
	}
	n := 5
	if a.Max != "" {
		if v, err := strconv.Atoi(a.Max); err == nil && v > 0 {
			if v > 10 {
				v = 10
			}
			n = v
		}
	}
	d := 20 * time.Second
	u := ddgEndpoint + url.QueryEscape(q)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36")
	client := &http.Client{Timeout: d}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 300000))
	if err != nil {
		return "", err
	}
	var out strings.Builder
	found := 0
	rest := string(body)
	for found < n {
		i := strings.Index(rest, `class="result__a"`)
		if i < 0 {
			break
		}
		seg := rest[i:]
		h := strings.Index(seg, `href="`)
		if h < 0 {
			break
		}
		h += len(`href="`)
		he := strings.Index(seg[h:], `"`)
		if he < 0 {
			break
		}
		link := seg[h : h+he]
		if iu := strings.Index(link, "uddg="); iu >= 0 {
			v := link[iu+len("uddg="):]
			if j := strings.IndexAny(v, "&"); j >= 0 {
				v = v[:j]
			}
			if un, err := url.QueryUnescape(v); err == nil {
				link = un
			}
		}
		ts := h + he + 1
		if strings.Index(seg[ts:], `>`) < 0 {
			break
		}
		ts += strings.Index(seg[ts:], `>`) + 1
		te := strings.Index(seg[ts:], `</a>`)
		if te < 0 {
			break
		}
		title := cleanHTML(seg[ts : ts+te])
		snippet := ""
		srest := seg[ts+te:]
		si := strings.Index(srest, `class="result__snippet"`)
		if si >= 0 {
			sr := srest[si:]
			sb := strings.Index(sr, `>`)
			se := strings.Index(sr, `</a>`)
			if sb >= 0 && se > sb {
				snippet = cleanHTML(sr[sb+1 : se])
			}
		}
		if title != "" || link != "" {
			found++
			fmt.Fprintf(&out, "%d. %s\n%s%s\n", found, title, snippet, link)
			if snippet != "" {
				fmt.Fprintf(&out, "   %s\n", snippet)
			}
		}
		rest = seg[ts+te+len("</a>"):]
	}
	if found == 0 {
		return "", fmt.Errorf("sin resultados para %q", q)
	}
	return out.String(), nil
}

func cleanHTML(s string) string {
	r := strings.NewReplacer(
		"&amp;", "&", "&quot;", `"`, "&#39;", "'", "&lt;", "<", "&gt;", ">", "&nbsp;", " ",
	)
	return strings.TrimSpace(r.Replace(s))
}

func gitDir(ws *Workspace, a toolArgs) string {
	if a.Path == "" {
		return ws.Cwd()
	}
	return ws.Resolve(a.Path)
}

func gitExec(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = runtimeEnv()
	out, err := cmd.CombinedOutput()
	s := truncate(string(out), 20000)
	if err != nil {
		return s, fmt.Errorf("git %v: %v", args, err)
	}
	return s, nil
}

func gitStatus(ctx context.Context, ws *Workspace, a toolArgs) (string, error) {
	return gitExec(ctx, gitDir(ws, a), "status", "--short", "--branch")
}

func gitBranch(ctx context.Context, ws *Workspace, a toolArgs) (string, error) {
	s, err := gitExec(ctx, gitDir(ws, a), "branch", "--show-current")
	if err != nil {
		return "", err
	}
	if s == "" {
		return "(sin rama)", nil
	}
	return s, nil
}

func gitLog(ctx context.Context, ws *Workspace, a toolArgs) (string, error) {
	n := 10
	if a.N != "" {
		if v, err := strconv.Atoi(a.N); err == nil && v > 0 {
			if v > 50 {
				v = 50
			}
			n = v
		}
	}
	return gitExec(ctx, gitDir(ws, a), "log", "--oneline", "-n", strconv.Itoa(n))
}

func gitDiff(ctx context.Context, ws *Workspace, a toolArgs) (string, error) {
	return gitExec(ctx, gitDir(ws, a), "diff")
}
