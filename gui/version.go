package main

import (
	"os"
	"path/filepath"
	"strings"
)

func appVersionValue() string {
	if v := strings.TrimSpace(appVersion); v != "" {
		return v
	}
	for _, path := range []string{filepath.Join("..", "VERSION"), "VERSION"} {
		if raw, err := os.ReadFile(path); err == nil {
			if v := strings.TrimSpace(string(raw)); v != "" {
				return v
			}
		}
	}
	return "unknown"
}
