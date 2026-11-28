package badcore

import (
	s "sort"
	"strings"
)

// Notes holds raw text and slices behind standard-library named types. The zero Notes is no notes.
type Notes struct {
	lines s.StringSlice   // core-primitive-field
	buf   strings.Builder // core-primitive-field
}

// NewNotes takes raw text behind a standard-library slice name.
func NewNotes(lines s.StringSlice, w Words) Notes { return Notes{lines: lines} } // core-primitive-signature

// Merge takes a standard-library named slice in a method signature.
func (n Notes) Merge(lines s.StringSlice) Notes { return n } // core-primitive-signature, core-collection
