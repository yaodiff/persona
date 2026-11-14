package nodekit

import (
	"strings"

	"github.com/nwverse/persona/server/internal/textfmt"
)

// FirstLine is the first line of s, cut to 200 bytes on a whole character.
func FirstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return textfmt.Cut(s, 200)
}
