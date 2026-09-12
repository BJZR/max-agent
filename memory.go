package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const memBlockPrefix = "[MEMORIA"

const memMaxChars = 3000

type Memory struct {
	mu      sync.Mutex
	file    string
	entries []string
}

func memoryPath() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".max", "memory.md")
	}
	return ".max-memory.md"
}

func loadMemory() *Memory {
	return loadMemoryPath(memoryPath())
}

func loadMemoryPath(p string) *Memory {
	m := &Memory{file: p}
	data, err := os.ReadFile(p)
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			m.entries = append(m.entries, line)
		}
	}
	return m
}

func (m *Memory) List() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.entries))
	copy(out, m.entries)
	return out
}

func (m *Memory) Add(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries {
		if e == text {
			return
		}
	}
	m.entries = append(m.entries, text)
	m.writeLocked()
}

func (m *Memory) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = nil
	m.writeLocked()
}

func (m *Memory) writeLocked() {
	data := []byte(strings.Join(m.entries, "\n") + "\n")
	if len(m.entries) == 0 {
		data = []byte{}
	}
	os.MkdirAll(filepath.Dir(m.file), 0o755)
	os.WriteFile(m.file, data, 0o644)
}

func (m *Memory) Block() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[MEMORIA (persistente entre sesiones)]\n")
	total := 0
	for _, e := range m.entries {
		line := "- " + e + "\n"
		if total+len(line) > memMaxChars {
			b.WriteString("- (y " + itoa(len(m.entries)-total) + " hechos más)\n")
			break
		}
		b.WriteString(line)
		total += len(line)
	}
	return strings.TrimSpace(b.String())
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}
