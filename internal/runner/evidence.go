package runner

import (
	"encoding/json"
)

// Evidence size caps (GRID_SPEC §5.4): 64 KB per field, 1 MB total across a
// submission's results. Over cap the server answers 413 and the CLI
// re-truncates at half the cap and retries once — Retruncate(evidence, 0.5).
const (
	// MaxFieldBytes caps any single evidence field's serialised size.
	MaxFieldBytes = 64 << 10
	// MaxTotalBytes caps one check's whole evidence payload.
	MaxTotalBytes = 1 << 20
	// ExcerptLimit is the §4 file.contains excerpt head cap (first 32 KB,
	// or ±5 lines around the first match).
	ExcerptLimit = MaxFieldBytes / 2
)

// Evidence is the measurement payload one check uploads (§4 "Evidence the
// CLI uploads" column, field-for-field per check type). Truncation adds a
// top-level "truncated": true sibling — the backend's evidence validators
// accept it as an optional boolean and use it to caveat failures.
type Evidence map[string]any

// BuildEvidence assembles an evidence payload from a field map, applying
// the §5.4 caps head-biased: a string or byte-slice field longer than
// MaxFieldBytes keeps its head and sets "truncated": true; then the whole
// payload is held under MaxTotalBytes by halving the per-field budget (head
// keeps winning over tail). Composite values (entries[], commits[]) are
// bounded by their own §4 caps (500 / 50) and are not byte-truncated here.
func BuildEvidence(fields map[string]any) Evidence {
	ev := Evidence{}
	truncated := false
	for k, v := range fields {
		switch t := v.(type) {
		case string:
			capped := truncateUTF8([]byte(t), MaxFieldBytes)
			if len(capped) < len(t) {
				truncated = true
			}
			ev[k] = string(capped)
		case []byte:
			capped := truncateUTF8(t, MaxFieldBytes)
			if len(capped) < len(t) {
				truncated = true
			}
			ev[k] = capped
		default:
			ev[k] = v
		}
	}
	if enforceTotalCap(ev, MaxTotalBytes, MaxFieldBytes) {
		truncated = true
	}
	if truncated {
		ev["truncated"] = true
	}
	return ev
}

// Retruncate re-caps an evidence payload at fraction of the normal caps —
// the 413 path (§5.4): the server rejected the payload as oversized, so the
// CLI re-truncates every text field at fraction*MaxFieldBytes (head-biased)
// and re-enforces the total at fraction*MaxTotalBytes, then retries once.
// The canonical call is Retruncate(ev, 0.5).
func Retruncate(ev Evidence, fraction float64) Evidence {
	if fraction <= 0 || fraction > 1 {
		fraction = 0.5
	}
	fieldBudget := int(float64(MaxFieldBytes) * fraction)
	totalBudget := int(float64(MaxTotalBytes) * fraction)
	out := Evidence{}
	truncated := false
	for k, v := range ev {
		switch t := v.(type) {
		case string:
			capped := truncateUTF8([]byte(t), fieldBudget)
			if len(capped) < len(t) {
				truncated = true
			}
			out[k] = string(capped)
		case []byte:
			capped := truncateUTF8(t, fieldBudget)
			if len(capped) < len(t) {
				truncated = true
			}
			out[k] = capped
		default:
			out[k] = v
		}
	}
	if enforceTotalCap(out, totalBudget, fieldBudget) {
		truncated = true
	}
	if truncated {
		out["truncated"] = true
	}
	return out
}

// Size returns the evidence payload's serialised size in bytes — the measure
// the §5.4 caps (and the backend's totalEvidenceBytes) are taken against.
func Size(ev Evidence) int {
	b, err := json.Marshal(ev)
	if err != nil {
		return 0
	}
	return len(b)
}

// enforceTotalCap shrinks the per-field budget (halving, floor 1 KB) until
// the serialised payload fits the total budget, re-truncating every text
// field head-biased each round. Order-independent: every text field is held
// to the same budget, whatever the map iteration order. Reports whether any
// field was shortened by this pass, so the caller can set "truncated": true
// even when no individual field exceeded the per-field cap on its own.
func enforceTotalCap(ev Evidence, totalBudget, fieldBudget int) bool {
	truncated := false
	for Size(ev) > totalBudget && fieldBudget > 1024 {
		fieldBudget /= 2
		for k, v := range ev {
			switch t := v.(type) {
			case string:
				capped := truncateUTF8([]byte(t), fieldBudget)
				if len(capped) < len(t) {
					truncated = true
				}
				ev[k] = string(capped)
			case []byte:
				capped := truncateUTF8(t, fieldBudget)
				if len(capped) < len(t) {
					truncated = true
				}
				ev[k] = capped
			}
		}
	}
	return truncated
}

// truncateUTF8 head-truncates b to at most limit bytes, backing off to the
// last complete rune so a truncated multi-byte character is never split.
func truncateUTF8(b []byte, limit int) []byte {
	if len(b) <= limit {
		return b
	}
	cut := limit
	for cut > 0 && cut > limit-4 && !startsRune(b[cut]) {
		cut--
	}
	return b[:cut]
}

// startsRune reports whether byte b begins a UTF-8 rune (not a continuation
// byte 0b10xxxxxx).
func startsRune(b byte) bool { return b&0xC0 != 0x80 }

// FileExcerpt builds a §4 file.contains excerpt, head-biased: when the
// caller found a pattern match (firstMatch is the byte index of its first
// occurrence in the — already normalised — content, or -1 when there is
// none), the excerpt is ±5 lines around that first match; otherwise it is
// the first ExcerptLimit (32 KB) bytes. The second return value reports
// whether content beyond the excerpt was dropped (the caller sets
// "truncated": true for it). Pass 5's checker compiles the pattern with the
// multiline flag always on (see normalize.go) and locates the match; the
// excerpting contract lives here.
func FileExcerpt(content []byte, firstMatch int) ([]byte, bool) {
	if firstMatch >= 0 && firstMatch < len(content) {
		lines := splitLines(content)
		idx, consumed := 0, 0
		for i, line := range lines {
			if firstMatch <= consumed+len(line) {
				idx = i
				break
			}
			consumed += len(line) + 1 // line + its newline
		}
		lo := max(idx-5, 0)
		hi := min(idx+6, len(lines)) // exclusive: the match line plus five after it
		var excerpt []byte
		for i, line := range lines[lo:hi] {
			if i > 0 {
				excerpt = append(excerpt, '\n')
			}
			excerpt = append(excerpt, line...)
		}
		capped := truncateUTF8(excerpt, ExcerptLimit)
		return capped, lo > 0 || hi < len(lines) || len(capped) < len(content)
	}
	capped := truncateUTF8(content, ExcerptLimit)
	return capped, len(capped) < len(content)
}

// splitLines splits content on \n, dropping the trailing empty element a
// final newline would produce.
func splitLines(content []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range content {
		if b == '\n' {
			lines = append(lines, content[start:i])
			start = i + 1
		}
	}
	if start < len(content) {
		lines = append(lines, content[start:])
	}
	return lines
}
