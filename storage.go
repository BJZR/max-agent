package main

import (
	"encoding/json"
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
		return s
	}
	json.Unmarshal(data, &s)
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
	return os.WriteFile(p, data, 0o644)
}
