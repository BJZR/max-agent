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
	"strings"
	"sync"
)

type webApp struct {
	cfg      Config
	prov     *Provider
	system   string
	mu       sync.Mutex
	pending  map[string]chan bool
	sessions map[string][]Msg
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
		cfg:      cfg,
		prov:     prov,
		system:   system,
		pending:  map[string]chan bool{},
		sessions: map[string][]Msg{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handleStatic)
	mux.HandleFunc("/api/model", app.handleModel)
	mux.HandleFunc("/api/info", app.handleInfo)
	mux.HandleFunc("/api/chat", app.handleChat)
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
	})
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
	used := p.Session != "" && history != nil
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

	agent := &Agent{
		prov:     app.prov,
		config:   app.cfg,
		system:   app.system,
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
