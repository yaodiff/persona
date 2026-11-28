package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// NoteID names one note. Only the book mints one (ParseNoteID reads one back); the zero NoteID is no note.
type NoteID string

// ParseNoteID reads an id as the book wrote it: "n" and a positive number.
func ParseNoteID(s string) (NoteID, error) {
	n, err := strconv.ParseUint(strings.TrimPrefix(s, "n"), 10, 63)
	if !strings.HasPrefix(s, "n") || err != nil || n == 0 || s != mintNoteID(n).String() {
		return "", fmt.Errorf("%w: %q", errBadNoteID, s)
	}
	return NoteID(s), nil
}

func mintNoteID(n uint64) NoteID { return NoteID("n" + strconv.FormatUint(n, 10)) }

func (id NoteID) String() string { return string(id) }

// Mark is how a note stands out: a closed set. The zero Mark is not a mark; no stored note has it.
type Mark uint8

const (
	MarkPlain Mark = iota + 1
	MarkImportant
)

// markNames is the set as it is written in the file and on the route.
var markNames = map[Mark]string{MarkPlain: "plain", MarkImportant: "important"}

// ParseMark reads a mark's name; a name outside the set is an error, never a default.
func ParseMark(s string) (Mark, error) {
	for m, name := range markNames {
		if name == s {
			return m, nil
		}
	}
	return 0, fmt.Errorf("not a mark: %q (plain or important)", s)
}

func (m Mark) String() string { return markNames[m] }

// Toggled is the other mark.
func (m Mark) Toggled() Mark {
	if m == MarkImportant {
		return MarkPlain
	}
	return MarkImportant
}

func (m Mark) MarshalText() ([]byte, error) {
	if _, ok := markNames[m]; !ok {
		return nil, fmt.Errorf("not a mark: %d", m)
	}
	return []byte(m.String()), nil
}

func (m *Mark) UnmarshalText(b []byte) error {
	v, err := ParseMark(string(b))
	if err != nil {
		return err
	}
	*m = v
	return nil
}

// Note is one stored note, as it is in the file and on the route.
type Note struct {
	ID      NoteID    `json:"id"`
	Text    string    `json:"text"`
	Mark    Mark      `json:"mark"`
	Created time.Time `json:"created_at"`
}

// maxNoteText and maxNotes bound the book: a sample keeps its file small.
const (
	maxNoteText = 500
	maxNotes    = 200
)

var (
	errBadNoteID = errors.New("not a note id")
	errEmptyNote = errors.New("a note needs some text")
	errLongNote  = fmt.Errorf("a note is at most %d characters", maxNoteText)
	errFullBook  = fmt.Errorf("the book holds at most %d notes", maxNotes)
	errNoNote    = errors.New("no such note")
)

// bookFile is the stored value: one JSON file in the extension's data directory. The hub keeps that directory
// while the extension is switched off and across upgrades; only an uninstall without --keep-data removes it.
type bookFile struct {
	Next  uint64 `json:"next"`
	Notes []Note `json:"notes"`
}

// Book is the notes, newest first, kept in one file.
type Book struct {
	mu   sync.Mutex
	path string
}

// OpenBook is the book in dir (the extension's data directory).
func OpenBook(dir string) *Book { return &Book{path: filepath.Join(dir, "notes.json")} }

func (b *Book) read() (bookFile, error) {
	var f bookFile
	raw, err := os.ReadFile(b.path)
	if errors.Is(err, os.ErrNotExist) {
		return bookFile{Next: 1}, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, fmt.Errorf("%s: %w", filepath.Base(b.path), err)
	}
	return f, nil
}

// write replaces the file whole: a reader never sees half of it.
func (b *Book) write(f bookFile) error {
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		return err
	}
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, b.path)
}

// List is every note, newest first.
func (b *Book) List() ([]Note, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f, err := b.read()
	if err != nil {
		return nil, err
	}
	out := make([]Note, 0, len(f.Notes))
	for i := len(f.Notes) - 1; i >= 0; i-- {
		out = append(out, f.Notes[i])
	}
	return out, nil
}

// Add stores a note written at now.
func (b *Book) Add(text string, mark Mark, now time.Time) (Note, error) {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return Note{}, errEmptyNote
	case utf8.RuneCountInString(text) > maxNoteText:
		return Note{}, errLongNote
	}
	if _, ok := markNames[mark]; !ok {
		return Note{}, fmt.Errorf("not a mark: %d", mark)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	f, err := b.read()
	if err != nil {
		return Note{}, err
	}
	if len(f.Notes) >= maxNotes {
		return Note{}, errFullBook
	}
	n := Note{ID: mintNoteID(f.Next), Text: text, Mark: mark, Created: now.UTC().Truncate(time.Second)}
	f.Next++
	f.Notes = append(f.Notes, n)
	return n, b.write(f)
}

// Toggle switches a note's mark and answers the note as stored.
func (b *Book) Toggle(id NoteID) (Note, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f, err := b.read()
	if err != nil {
		return Note{}, err
	}
	for i := range f.Notes {
		if f.Notes[i].ID == id {
			f.Notes[i].Mark = f.Notes[i].Mark.Toggled()
			return f.Notes[i], b.write(f)
		}
	}
	return Note{}, errNoNote
}
