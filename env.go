package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
)

func envSnapshot() string {
	var b strings.Builder
	cwd, _ := os.Getwd()
	fmt.Fprintf(&b, "directorio de trabajo: %s\n", cwd)
	fmt.Fprintf(&b, "sistema: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	if u := os.Getenv("USER"); u != "" {
		fmt.Fprintf(&b, "usuario: %s\n", u)
	}
	if out, err := exec.Command("git", "status", "--short", "--branch", "--porcelain=v1").CombinedOutput(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			fmt.Fprintf(&b, "git:\n%s\n", truncate(s, 600))
		}
	}
	if entries, err := os.ReadDir(cwd); err == nil {
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		var names []string
		for _, e := range entries {
			n := e.Name()
			if strings.HasPrefix(n, ".") {
				continue
			}
			if e.IsDir() {
				n += "/"
			}
			names = append(names, n)
		}
		if len(names) > 30 {
			fmt.Fprintf(&b, "archivos en cwd: %s", strings.Join(names[:30], " "))
			fmt.Fprintf(&b, "\n  ... %d entradas más", len(names)-30)
		} else {
			fmt.Fprintf(&b, "archivos en cwd: %s", strings.Join(names, " "))
		}
	}
	return strings.TrimSpace(b.String())
}
