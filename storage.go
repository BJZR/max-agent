package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

type sessionData struct {
	Updated  time.Time `json:"updated"`
	Cwd      string    `json:"cwd"`
	Messages []Msg     `json:"messages"`
}

type store map[string]sessionData

func storePath() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".max", "sessions.json")
	}
	return ".max-sessions.json"
}

func loadStore() store {
	return loadStorePath(storePath())
}

func loadStorePath(p string) store {
	s := store{}
	data, err := os.ReadFile(p)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("max: no se pudo leer %s: %v", p, err)
		}
		return s
	}
	if err := json.Unmarshal(data, &s); err != nil {
		log.Printf("max: sesiones corruptas en %s (%v); se respalda a .bak", p, err)
		_ = os.WriteFile(p+".bak", data, 0o644)
		return s
	}
	return s
}

func (s store) Save() error {
	return s.savePath(storePath())
}

func (s store) savePath(p string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
