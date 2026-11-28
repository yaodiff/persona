package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The page's actions: a closed set of ids, the note's id after the colon where one is named.
const (
	actionAdd    = "add"
	actionToggle = "toggle:"
)

// app is the extension: one book, served to the page and to the route.
type app struct {
	book *Book
	now  func() time.Time
}

func (a *app) init(ctx context.Context, in *ext.Init, hub *ext.Hub) (map[string]any, error) {
	dir := in.Info().DataDir
	if dir == "" {
		dir = ext.Env().Data
	}
	if dir == "" {
		return nil, errors.New("the hub gave no data directory")
	}
	a.book = OpenBook(dir)
	return nil, nil
}

// home draws the page: a field to write a note, then the notes, newest first.
func (a *app) home(ctx context.Context, r *ext.UIRequest) (*wire.View, error) {
	l := langOf(r.Locale)
	notes, err := a.book.List()
	if err != nil {
		return nil, err
	}
	rows := make([]wire.Component, 0, len(notes))
	for _, n := range notes {
		row := wire.Component{Type: wire.ComponentRow, Title: n.Text, Icon: "doc", Value: n.Created.Format("2006-01-02"),
			Action: &wire.Action{ID: actionToggle + n.ID.String()}}
		if n.Mark == MarkImportant {
			row.Badge = &wire.Badge{Text: TextImportant.In(l), Tone: wire.ToneCaution}
		}
		rows = append(rows, row)
	}
	footer := TextTapToMark.In(l)
	if len(rows) == 0 {
		rows, footer = append(rows, wire.Component{Type: wire.ComponentEmpty, Title: TextEmpty.In(l), Icon: "doc"}), ""
	}
	return &wire.View{Title: TextTitle.In(l), Sections: []wire.Section{
		{Title: TextNewNote.In(l), Footer: TextStoredFooter.In(l), Items: []wire.Component{
			{Type: wire.ComponentField, Name: "text", Label: TextNewNote.In(l), Kind: wire.FieldKindText, Placeholder: TextPlaceholder.In(l)},
			{Type: wire.ComponentButton, Title: TextAdd.In(l), Action: &wire.Action{ID: actionAdd, Submit: true}},
		}},
		{Title: TextNotes.In(l), Footer: footer, Items: rows},
	}}, nil
}

// act answers a tap; an empty result draws the page again.
func (a *app) act(ctx context.Context, r *ext.UIRequest) (*wire.ActionResult, error) {
	lang := langOf(r.Locale)
	switch {
	case r.Action == actionAdd:
		if _, err := a.book.Add(r.Values["text"], MarkPlain, a.now()); err != nil {
			return nil, userError(err, lang)
		}
	case strings.HasPrefix(r.Action, actionToggle):
		id, err := ParseNoteID(strings.TrimPrefix(r.Action, actionToggle))
		if err != nil {
			return nil, userError(err, lang)
		}
		if _, err := a.book.Toggle(id); err != nil {
			return nil, userError(err, lang)
		}
	default:
		return nil, &ext.Error{Code: "invalid", Message: TextUnknownAction.In(lang)}
	}
	return nil, nil
}

// userError is err as the app shows it: the book's own refusals are the user's to read, anything else is internal.
func userError(err error, lang Lang) error {
	var text Text
	switch {
	case errors.Is(err, errNoNote), errors.Is(err, errBadNoteID):
		text = TextNoNote
	case errors.Is(err, errEmptyNote):
		text = TextNeedsText
	case errors.Is(err, errLongNote):
		text = TextNoteTooLong
	case errors.Is(err, errFullBook):
		text = TextBookFull
	default:
		return err
	}
	code := "invalid"
	if text == TextNoNote {
		code = "not_found"
	}
	return &ext.Error{Code: code, Message: text.In(lang)}
}
