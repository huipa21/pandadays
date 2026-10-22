// Package portability exports a user's own days as a zip (days.csv + media)
// and imports such an archive (or a bare days.csv) in merge mode (SPEC §6).
package portability

import (
	"strings"
)

// Columns is the days.csv header (SPEC §6.1).
var Columns = []string{"title", "date", "time", "repeat", "collection", "tags", "comment",
	"archived", "style_color", "style_icon", "style_bg", "attachments", "archive_live"}

const (
	csvName     = "days.csv"
	mediaPrefix = "media/"
	utf8BOM     = "\ufeff"
)

func isFormulaTrigger(c byte) bool {
	switch c {
	case '=', '+', '-', '@', '\t', '\r':
		return true
	}
	return false
}

// escapeCell guards against spreadsheet formula injection. A leading run of
// quotes is included so that unescapeCell is its exact inverse: a cell that
// already looks escaped ("'=x") gains another quote ("”=x").
func escapeCell(s string) string {
	rest := strings.TrimLeft(s, "'")
	if rest != "" && isFormulaTrigger(rest[0]) {
		return "'" + s
	}
	return s
}

func unescapeCell(s string) string {
	rest := strings.TrimLeft(s, "'")
	if len(rest) < len(s) && rest != "" && isFormulaTrigger(rest[0]) {
		return s[1:]
	}
	return s
}

// joinList joins list cells with '|'; '\' and '|' inside items are backslash-escaped.
func joinList(items []string) string {
	escaped := make([]string, len(items))
	for i, item := range items {
		item = strings.ReplaceAll(item, `\`, `\\`)
		escaped[i] = strings.ReplaceAll(item, "|", `\|`)
	}
	return strings.Join(escaped, "|")
}

// splitList is the inverse of joinList. Items are trimmed and empty items dropped.
func splitList(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if item := strings.TrimSpace(cur.String()); item != "" {
			out = append(out, item)
		}
		cur.Reset()
	}
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
		case s[i] == '|':
			flush()
		default:
			cur.WriteByte(s[i])
		}
	}
	flush()
	return out
}
