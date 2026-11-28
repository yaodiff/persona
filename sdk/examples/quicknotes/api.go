package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// The route the manifest's api contribution names: the hub authenticates a caller of /api/v1/quicknotes/… and
// forwards the request here with the path unchanged (docs/extensions/extensions.md "App endpoints"). While the
// extension is switched off the hub answers 404 itself and the feature "quicknotes" leaves its hello.

// notesAnswer is GET /api/v1/quicknotes/notes.
type notesAnswer struct {
	Notes []Note `json:"notes"`
}

// newNote is the body of POST /api/v1/quicknotes/notes. Mark may be left out: a plain note.
type newNote struct {
	Text string `json:"text"`
	Mark *Mark  `json:"mark,omitempty"`
}

type apiError struct {
	Error string `json:"error"`
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/quicknotes/notes", a.listNotes)
	mux.HandleFunc("POST /api/v1/quicknotes/notes", a.addNote)
	return mux
}

func (a *app) listNotes(w http.ResponseWriter, r *http.Request) {
	notes, err := a.book.List()
	if err != nil {
		answer(w, http.StatusInternalServerError, apiError{Error: "the notes could not be read"})
		return
	}
	answer(w, http.StatusOK, notesAnswer{Notes: notes})
}

func (a *app) addNote(w http.ResponseWriter, r *http.Request) {
	// Read strictly, once: a field the route does not know, a mark outside the set or anything after the object
	// refuses the request.
	var in newNote
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		answer(w, http.StatusBadRequest, apiError{Error: "invalid body: " + err.Error()})
		return
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		answer(w, http.StatusBadRequest, apiError{Error: "invalid body: one JSON object"})
		return
	}
	mark := MarkPlain
	if in.Mark != nil {
		mark = *in.Mark
	}
	n, err := a.book.Add(in.Text, mark, a.now())
	switch {
	case errors.Is(err, errEmptyNote), errors.Is(err, errLongNote), errors.Is(err, errFullBook):
		answer(w, http.StatusBadRequest, apiError{Error: err.Error()})
	case err != nil:
		answer(w, http.StatusInternalServerError, apiError{Error: "the note could not be stored"})
	default:
		answer(w, http.StatusCreated, n)
	}
}

func answer(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
