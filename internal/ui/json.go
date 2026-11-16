// Package ui renders all human-facing output: colour theming, tables,
// spinners, verdict rendering, and the --json machine output helpers.
//
// This file (json.go) owns the one encoder every command's --json path
// uses (pass 9 polish: the identical three-line `json.NewEncoder(os.Stdout);
// enc.SetEscapeHTML(false); enc.Encode(...)` body was duplicated across
// eleven cmd/*.go call sites before this). WriteJSON always marshals a
// typed struct the caller already built (api.SubmissionResponse,
// checkJSON, loginJSON, ...) — never an ad-hoc map — so field order stays
// stable across releases (TASK.md §3: "a stable, documented schema").
package ui

import (
	"encoding/json"
	"os"
)

// WriteJSON marshals v to stdout only, on its own line, with HTML-escaping
// disabled so URLs and comparison operators in evidence/messages render
// unescaped. Every command's --json branch calls this and nothing else —
// diagnostics and progress never go through it, only to stderr directly
// (TASK.md §3: "--json output on stdout... diagnostics and progress go to
// stderr").
func WriteJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
