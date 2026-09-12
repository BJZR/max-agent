package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type webApp struct {
	cfg        Config
	prov       *Provider
	system     string
	mu         sync.Mutex
	pending    map[string]chan bool
	sessions   map[string][]Msg
	workspaces map[string]*Workspace
}

type webPayload struct {
	Session  string `json:"session"`
	Text     string `json:"text"`
	Messages []Msg  `json:"messages"`
}

type sseMsg struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Name string `json:"name,omitempty"`
	Args string `json:"args,omitempty"`
	ID   string `json:"id,omitempty"`
}

type webApprover struct {
	app   *webApp
	ctx   context.Context
	auto  bool
	write func(sseMsg) error
}

func (w *webApprover) Approve(name, args string) (bool, error) {
	if !dangerous[name] || w.auto {
		if w.write != nil {
			w.write(sseMsg{Type: "tool", Name: name, Args: truncate(args, 500)})
		}
		return true, nil
	}
	id, err := newID()
	if err != nil {
		return false, err
	}
	ch := make(chan bool, 1)
	w.app.mu.Lock()
	w.app.pending[id] = ch
	w.app.mu.Unlock()
	defer func() {
		w.app.mu.Lock()
		delete(w.app.pending, id)
		w.app.mu.Unlock()
	}()
	if w.write != nil {
		w.write(sseMsg{Type: "tool", Name: name, Args: truncate(args, 500), ID: id})
	}
	select {
	case <-w.ctx.Done():
		return false, w.ctx.Err()
	case v := <-ch:
		return v, nil
	}
}

func newID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func runWeb(cfg Config, prov *Provider, system string) {
	app := &webApp{
		cfg:        cfg,
		prov:       prov,
		system:     system,
		pending:    map[string]chan bool{},
		sessions:   map[string][]Msg{},
		workspaces: map[string]*Workspace{},
	}

	if cfg.Persist {
		st := loadStore()
		for sid, d := range st {
			if len(d.Messages) == 0 {
				continue
			}
			ws := newWorkspace()
			if d.Cwd != "" {
				ws.SetCwd(d.Cwd)
			}
			app.workspaces[sid] = ws
			app.sessions[sid] = d.Messages
		}
		if len(st) > 0 {
			log.Printf("restauradas %d sesiones desde disco", len(st))
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handleStatic)
	mux.HandleFunc("/api/model", app.handleModel)
	mux.HandleFunc("/api/info", app.handleInfo)
	mux.HandleFunc("/api/chat", app.handleChat)
	mux.HandleFunc("/api/sessions", app.handleSessions)
	mux.HandleFunc("/api/sessions/", app.handleSession)
	mux.HandleFunc("/api/pending", app.handlePendingList)
	mux.HandleFunc("/api/pending/", app.handlePendingAction)

	addr := cfg.ServerAddr
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "localhost" + host
	}
	fmt.Printf("\033[1;32mMAX\033[0m web: http://%s\nmodelo: %s\n", host, cfg.Model)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func (app *webApp) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "ui no encontrada", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (app *webApp) handleModel(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{"model": app.cfg.Model})
}

func (app *webApp) handleInfo(w http.ResponseWriter, r *http.Request) {
	cwd, _ := os.Getwd()
	json.NewEncoder(w).Encode(map[string]any{
		"model": app.cfg.Model,
		"cwd":   cwd,
		"tools": app.cfg.Tools,
		"shell": app.cfg.Shell,
	})
}

func sessionTitle(msgs []Msg) string {
	for _, m := range msgs {
		if m.Role == "user" && !strings.HasPrefix(m.Content, "[Contexto del entorno") {
			return truncate(strings.ReplaceAll(m.Content, "\n", " "), 60)
		}
	}
	return "sesión"
}

func (app *webApp) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		app.mu.Lock()
		defer app.mu.Unlock()
		st := loadStore()
		list := make([]map[string]any, 0, len(app.sessions)+len(st))
		seen := map[string]bool{}
		for sid, msgs := range app.sessions {
			if len(msgs) == 0 {
				continue
			}
			seen[sid] = true
			cwd := ""
			if ws := app.workspaces[sid]; ws != nil {
				cwd = ws.Cwd()
			}
			list = append(list, map[string]any{"id": sid, "updated": st[sid].Updated, "title": sessionTitle(msgs), "cwd": cwd})
		}
		for sid, d := range st {
			if len(d.Messages) == 0 || seen[sid] {
				continue
			}
			list = append(list, map[string]any{"id": sid, "updated": d.Updated, "title": sessionTitle(d.Messages), "cwd": d.Cwd})
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i]["updated"].(time.Time).After(list[j]["updated"].(time.Time))
		})
		json.NewEncoder(w).Encode(list)
	case http.MethodPost:
		id, err := newID()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		app.mu.Lock()
		app.sessions[id] = []Msg{}
		app.workspaces[id] = newWorkspace()
		app.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"id": id, "messages": []Msg{}})
	default:
		http.Error(w, "método no permitido", http.StatusMethodNotAllowed)
	}
}

func (app *webApp) handleSession(w http.ResponseWriter, r *http.Request) {
	sid := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	if sid == "" {
		http.NotFound(w, r)
		return
	}
	app.mu.Lock()
	msgs, exists := app.sessions[sid]
	var cwd string
	if ws := app.workspaces[sid]; ws != nil {
		cwd = ws.Cwd()
	}
	app.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		if !exists {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": sid, "cwd": cwd, "messages": msgs, "updated": loadStore()[sid].Updated})
	case http.MethodDelete:
		app.mu.Lock()
		delete(app.sessions, sid)
		delete(app.workspaces, sid)
		app.mu.Unlock()
		if app.cfg.Persist {
			st := loadStore()
			delete(st, sid)
			if err := st.Save(); err != nil {
				log.Printf("borrar sesión: %v", err)
			}
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "{}")
	default:
		http.Error(w, "método no permitido", http.StatusMethodNotAllowed)
	}
}

func (app *webApp) handleChat(w http.ResponseWriter, r *http.Request) {
	var p webPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&p); err != nil {
		http.Error(w, "json inválido", http.StatusBadRequest)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "sin streaming", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fl.Flush()

	write := func(m sseMsg) error {
		data, _ := json.Marshal(m)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return err
		}
		fl.Flush()
		return nil
	}

	ctx := r.Context()
	var history []Msg
	if p.Session != "" {
		app.mu.Lock()
		history = app.sessions[p.Session]
		app.mu.Unlock()
	}
	used := p.Session != "" && len(history) > 0
	if !used {
		history = p.Messages
	}
	if !used && app.cfg.Context && app.cfg.Tools {
		history = append([]Msg{{Role: "user", Content: "[Contexto del entorno (dado por MAX)]\n" + envSnapshot()}}, history...)
	}

	sid := p.Session
	if sid == "" {
		var err error
		sid, err = newID()
		if err != nil {
			write(sseMsg{Type: "error", Text: err.Error()})
			return
		}
	}

	var ws *Workspace
	app.mu.Lock()
	ws = app.workspaces[sid]
	if ws == nil {
		ws = newWorkspace()
		app.workspaces[sid] = ws
	}
	app.mu.Unlock()

	agent := &Agent{
		prov:     app.prov,
		config:   app.cfg,
		system:   app.system,
		ws:       ws,
		approver: &webApprover{app: app, ctx: ctx, auto: app.cfg.AutoApprove, write: write},
		onToken:  func(t string) { write(sseMsg{Type: "token", Text: t}) },
		onToolOut: func(s string) {
			if s != "" {
				write(sseMsg{Type: "toolout", Text: truncate(s, 4000)})
			}
		},
	}

	content, newHist, err := agent.Chat(ctx, history, p.Text)
	if err != nil {
		write(sseMsg{Type: "error", Text: err.Error()})
	}
	write(sseMsg{Type: "done", Text: content, ID: sid})

	app.mu.Lock()
	if len(app.sessions) > 200 {
		for k := range app.sessions {
			delete(app.sessions, k)
			break
		}
	}
	app.sessions[sid] = newHist
	if app.cfg.Persist {
		d := sessionData{Updated: time.Now(), Cwd: ws.Cwd(), Messages: newHist}
		st := loadStore()
		st[sid] = d
		if err := st.Save(); err != nil {
			log.Printf("persistir sesión: %v", err)
		}
	}
	app.mu.Unlock()
}

func (app *webApp) handlePendingList(w http.ResponseWriter, r *http.Request) {
	app.mu.Lock()
	defer app.mu.Unlock()
	list := make([]map[string]string, 0, len(app.pending))
	for id := range app.pending {
		list = append(list, map[string]string{"id": id, "name": "aprobación pendiente", "args": ""})
	}
	json.NewEncoder(w).Encode(list)
}

func (app *webApp) handlePendingAction(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/pending/")
	var body struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "json inválido", http.StatusBadRequest)
		return
	}
	app.mu.Lock()
	ch := app.pending[id]
	app.mu.Unlock()
	if ch == nil {
		http.Error(w, "no encontrado", http.StatusNotFound)
		return
	}
	switch body.Action {
	case "accept", "approve", "ok":
		ch <- true
	case "deny", "reject", "no":
		ch <- false
	default:
		http.Error(w, "acción desconocida", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "{}")
}
