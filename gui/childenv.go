package main

// Child environment hygiene (PattN AetherProcess.IsCoreSetting).
//
// A user's shell may export AETHER_* / RUST_LOG for unrelated tools. The
// core reads those names, so inheriting them silently overrides the
// paths and verbosity this app computes. Strip them first, then append
// our own values: the last value wins for keys we set.

import "strings"

// sanitizeChildEnv drops every entry whose key matches a prefix or an
// exact name (case-insensitive, so Windows' case-insensitive environment
// behaves predictably). Entries without '=' are dropped too: they cannot
// be a valid environment variable.
func sanitizeChildEnv(base []string, prefixes, exact []string) []string {
	out := make([]string, 0, len(base))
	for _, kv := range base {
		// Windows keeps per-drive cwd entries as "=C:=C:\path"; they are
		// not NAME=value pairs and must survive untouched.
		if strings.HasPrefix(kv, "=") {
			out = append(out, kv)
			continue
		}
		name, _, ok := strings.Cut(kv, "=")
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		upper := strings.ToUpper(name)
		drop := false
		for _, p := range prefixes {
			if p != "" && strings.HasPrefix(upper, strings.ToUpper(p)) {
				drop = true
				break
			}
		}
		if !drop {
			for _, e := range exact {
				if e != "" && upper == strings.ToUpper(e) {
					drop = true
					break
				}
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}
