package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Workspace struct {
	mu  sync.Mutex
	cwd string
}

func newWorkspace() *Workspace {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	return &Workspace{cwd: cwd}
}

func (w *Workspace) Cwd() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cwd
}

func (w *Workspace) SetCwd(dir string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if p, err := filepath.Abs(dir); err == nil {
		w.cwd = p
	}
}

func (w *Workspace) Resolve(p string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if p == "" {
		return w.cwd
	}
	if filepath.IsAbs(p) || p == string(filepath.Separator) {
		return filepath.Clean(p)
	}
	return filepath.Join(w.cwd, p)
}

// cdIfNeeded devuelve (nuevoCwd, ok) si el comando es un simple "cd <dir>".
// Solo aplica si el comando es EXCLUSIVAMENTE un cd (sn línea o lista de comandos,
// p.ej. "cd c && gcc ..."), para no tragarse el resto del comando como ruta.
func cdIfNeeded(cmd string) (string, bool) {
	t := strings.TrimSpace(cmd)
	if !(t == "cd" || strings.HasPrefix(t, "cd ")) {
		return "", false
	}
	if strings.ContainsAny(t, "\n;&|") {
		return "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(t, "cd"))
	if rest == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		return h, true
	}
	if rest == "~" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		return h, true
	}
	rest = strings.Trim(rest, "'\"")
	return rest, true
}
