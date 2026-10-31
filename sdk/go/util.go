package sandbox

import (
	"fmt"
	"unicode/utf8"
)

// TruncateTail keeps the head and tail of a long string and inserts a marker
// in between. It never splits a UTF-8 rune and never returns more than limit
// bytes (plus the marker when truncation happens).
func TruncateTail(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	half := limit / 2
	head := text[:half]
	for len(head) > 0 && !utf8.ValidString(head) {
		head = head[:len(head)-1]
	}
	tail := text[len(text)-half:]
	for len(tail) > 0 && !utf8.ValidString(tail) {
		tail = tail[1:]
	}
	return fmt.Sprintf("%s\n…(%d bytes truncated)…\n%s", head, len(text)-len(head)-len(tail), tail)
}
