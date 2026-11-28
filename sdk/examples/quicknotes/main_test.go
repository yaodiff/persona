package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

var day = time.Date(2026, 11, 26, 9, 30, 0, 0, time.UTC)

func newApp(t *testing.T) (*app, string) {
	dir := t.TempDir()
	return &app{book: OpenBook(dir), now: func() time.Time { return day }}, filepath.Join(dir, "notes.json")
}

// The program holds to the extension protocol and every page draws a View the apps can show.
func TestConformance(t *testing.T) {
	t.Setenv("PERSONA_EXTENSION_DATA", t.TempDir())
	exttest.Check(t, manifest, exttest.Serve(t, program(&app{now: time.Now})))
}

// The manifest names what the document says the sample adds: one page, one route prefix, one feature flag.
func TestManifestDeclaresTheSample(t *testing.T) {
	var m struct {
		Contributes struct {
			UI  []struct{ ID, Placement string }
			API []struct {
				Prefix   string
				Features []string
			}
		}
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	c := m.Contributes
	if len(c.UI) != 1 || c.UI[0].ID != "quicknotes" || c.UI[0].Placement != "nav_page" {
		t.Fatalf("ui: %+v", c.UI)
	}
	if len(c.API) != 1 || c.API[0].Prefix != "quicknotes" || len(c.API[0].Features) != 1 || c.API[0].Features[0] != "quicknotes" {
		t.Fatalf("api: %+v", c.API)
	}
}

func TestPageAddsAndMarksNotes(t *testing.T) {
	a, _ := newApp(t)
	ctx := context.Background()
	r := &ext.UIRequest{}
	r.UIVersion = 3
	v, err := a.home(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.Sections[1].Items[0]; got.Type != wire.ComponentEmpty || got.Title != "No notes yet" {
		t.Fatalf("an empty book draws %+v", got)
	}
	r.Action, r.Values = actionAdd, map[string]string{"text": "  buy milk  "}
	if _, err := a.act(ctx, r); err != nil {
		t.Fatal(err)
	}
	v, _ = a.home(ctx, r)
	row := v.Sections[1].Items[0]
	if row.Title != "buy milk" || row.Value != "2026-11-26" || row.Badge != nil || row.Action.ID != "toggle:n1" {
		t.Fatalf("the note's row: %+v", row)
	}
	r.Action, r.Values = row.Action.ID, nil
	if _, err := a.act(ctx, r); err != nil {
		t.Fatal(err)
	}
	if v, _ = a.home(ctx, r); v.Sections[1].Items[0].Badge == nil || v.Sections[1].Items[0].Badge.Text != "Important" {
		t.Fatalf("a marked note: %+v", v.Sections[1].Items[0])
	}
	for action, values := range map[string]map[string]string{actionAdd: {"text": " "}, "toggle:n9": nil, "toggle:x": nil, "drop": nil} {
		r.Action, r.Values = action, values
		var e *ext.Error
		if _, err := a.act(ctx, r); !asExtError(err, &e) || e.Code == "" {
			t.Errorf("%s: %v, want an error with a code", action, err)
		}
	}
}

func asExtError(err error, e **ext.Error) bool {
	v, ok := err.(*ext.Error)
	*e = v
	return ok
}

// The page is written in the app's language: en, zh-Hans and ja (docs/protocol/i18n.md).
func TestEveryTextIsTranslated(t *testing.T) {
	for i := Text(0); i < textCount; i++ {
		for _, l := range []Lang{LangEN, LangZhHans, LangJA} {
			if i.In(l) == "" {
				t.Errorf("text %d has no words in language %d", i, l)
			}
		}
	}
	a, _ := newApp(t)
	for locale, title := range map[string]string{"": "Quick notes", "en-US": "Quick notes", "zh-Hans": "速记", "ja-JP": "クイックメモ", "fr": "Quick notes"} {
		r := &ext.UIRequest{}
		r.Locale = locale
		if v, _ := a.home(context.Background(), r); v.Title != title {
			t.Errorf("locale %q: title %q, want %q", locale, v.Title, title)
		}
	}
	r := &ext.UIRequest{}
	r.Action = actionAdd
	r.Values = map[string]string{"text": " "}
	for locale, want := range map[string]string{"en": "Enter some text for the note.", "zh-Hans": "请输入速记内容。", "ja-JP": "メモを入力してください。"} {
		r.Locale = locale
		_, err := a.act(context.Background(), r)
		var e *ext.Error
		if !asExtError(err, &e) || e.Message != want {
			t.Errorf("empty-note error in %q: %#v, want %q", locale, err, want)
		}
	}
	for _, f := range []string{"l10n/en.json", "l10n/zh-Hans.json", "l10n/ja.json"} {
		var l struct {
			Name string
			UI   map[string]string
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &l); err != nil || l.Name == "" || l.UI["quicknotes"] == "" {
			t.Errorf("%s: name %q, ui %v (%v)", f, l.Name, l.UI, err)
		}
	}
}

func TestRouteListsAndStoresNotes(t *testing.T) {
	a, _ := newApp(t)
	h := a.routes()
	call := func(method, body string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/quicknotes/notes", strings.NewReader(body)))
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	if code, body := call("GET", ""); code != 200 || body != `{"notes":[]}` {
		t.Fatalf("an empty book: %d %s", code, body)
	}
	if code, body := call("POST", `{"text":"call the bank","mark":"important"}`); code != http.StatusCreated ||
		body != `{"id":"n1","text":"call the bank","mark":"important","created_at":"2026-11-26T09:30:00Z"}` {
		t.Fatalf("a new note: %d %s", code, body)
	}
	if code, body := call("POST", `{"text":"plain one"}`); code != http.StatusCreated || !strings.Contains(body, `"mark":"plain"`) {
		t.Fatalf("a note without a mark: %d %s", code, body)
	}
	if code, body := call("GET", ""); code != 200 || !strings.HasPrefix(body, `{"notes":[{"id":"n2"`) {
		t.Fatalf("newest first: %d %s", code, body)
	}
	// Strict: a mark outside the set, an unknown field, an empty note and trailing data are refused, nothing stored.
	for _, bad := range []string{`{"text":"x","mark":"urgent"}`, `{"text":"x","colour":"red"}`, `{"text":"  "}`, `{"text":"x"} {}`, `[]`} {
		if code, body := call("POST", bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", bad, code, body)
		}
	}
	if notes, _ := a.book.List(); len(notes) != 2 {
		t.Fatalf("%d notes stored, want 2", len(notes))
	}
}

// What the document promises of a switch-off: the stored value is a plain file a new process reads as written, and
// reading changes none of its bytes.
func TestStoredNotesOutliveTheProgram(t *testing.T) {
	a, file := newApp(t)
	if _, err := a.book.Add("keep me", MarkImportant, day); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	again := OpenBook(filepath.Dir(file)) // the extension switched on again: a new process
	notes, err := again.List()
	if err != nil || len(notes) != 1 || notes[0].Text != "keep me" || notes[0].Mark != MarkImportant || notes[0].ID != "n1" {
		t.Fatalf("read back: %+v, %v", notes, err)
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(before, after) {
		t.Fatal("reading the notes changed the file")
	}
	if n, err := again.Add("second", MarkPlain, day); err != nil || n.ID != "n2" {
		t.Fatalf("the next id after a restart: %+v, %v", n, err)
	}
}

func TestClosedSetsRefuseWhatIsOutside(t *testing.T) {
	for _, s := range []string{"", "n", "n0", "n01", "7", "nx", "n-1"} {
		if id, err := ParseNoteID(s); err == nil {
			t.Errorf("ParseNoteID(%q) = %q", s, id)
		}
	}
	if id, err := ParseNoteID("n12"); err != nil || id != "n12" {
		t.Errorf("ParseNoteID(n12) = %q, %v", id, err)
	}
	for _, s := range []string{"", "Plain", "urgent"} {
		if m, err := ParseMark(s); err == nil {
			t.Errorf("ParseMark(%q) = %v", s, m)
		}
	}
	if _, err := json.Marshal(Note{ID: "n1", Text: "x"}); err == nil {
		t.Error("a note with the zero mark was written")
	}
	a, _ := newApp(t)
	if _, err := a.book.Add("x", Mark(9), day); err == nil {
		t.Error("a mark outside the set was stored")
	}
	if _, err := a.book.Add(strings.Repeat("é", maxNoteText+1), MarkPlain, day); err == nil {
		t.Error("a note over the limit was stored")
	}
}
