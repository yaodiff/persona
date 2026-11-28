package proto

import (
	"regexp"
	"strings"
	"sync"
)

// HerdrTabName returns the live tab label for an Agent in herdr or Herdr App.
func (s Session) HerdrTabName() string {
	if s.Assistant || s.SlotID == "" && s.TabID == "" {
		return ""
	}
	kind := s.SpaceKind
	if kind == "" {
		kind = s.Host
	}
	if kind != "" && kind != "herdr" && kind != "herdr-core" {
		return ""
	}
	return strings.TrimSpace(s.SlotName)
}

// RenamableTabName is the live tab label of an Agent in a host whose tab names Persona's naming renames, only while
// they still show the label it saw (#15674): herdr, the Herdr App and Orca (#17043). HerdrTabName stays herdr's alone:
// clients mirror it for what they display.
func (s Session) RenamableTabName() string {
	if kind := s.SpaceKind; kind == "orca" || kind == "" && s.Host == "orca" {
		if s.Assistant || s.SlotID == "" && s.TabID == "" {
			return ""
		}
		return strings.TrimSpace(s.SlotName)
	}
	return s.HerdrTabName()
}

// Keep this recognizer in sync with web/src/lib/format.ts. Whitespace after the
// colon is optional for absolute or home-relative paths, as in default bash titles.
// Compiled on first use with the standard library: a contract imports no internal/lazyre (#11793, as #5239).
var shellPromptTitle = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*@[A-Za-z0-9_][A-Za-z0-9_.-]*:(?:[ \t]+[^\r\n]+|[~/][^\r\n]*)$`)
})

// DisplayTabName returns the primary tab name when preferred or the CLI title is empty or a shell prompt.
func (s Session) DisplayTabName(preferTab bool) string {
	title := strings.TrimSpace(s.Title)
	if preferTab || title == "" || shellPromptTitle().MatchString(title) {
		return s.HerdrTabName()
	}
	return ""
}
