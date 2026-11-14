package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
)

const (
	savedAgentSearchesKey    = "agents.saved_searches"
	savedAgentSearchesMax    = 50
	savedAgentSearchNameMax  = 64
	savedAgentSearchQueryMax = 1024
)

// SavedAgentSearch is a named query, not an instruction or a subscription. Applying
// it is a client action and never starts an Agent or changes a session.
type SavedAgentSearch struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Query     string    `json:"query"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type savedAgentSearchDocument struct {
	Revision int64              `json:"revision"`
	Searches []SavedAgentSearch `json:"searches"`
}
type savedAgentSearchResponse struct {
	Revision string             `json:"revision"`
	Searches []SavedAgentSearch `json:"searches"`
	CanWrite bool               `json:"can_write"`
}

// Both reads and writes require durable revision support. A test/older store with
// only PutSetting must not advertise a collection that would lose concurrent edits.
type savedAgentSearchStore interface {
	Setting(context.Context, string, any) (bool, error)
	CompareSetting(context.Context, string, int64, any) (bool, error)
}
type savedAgentSearchMutation struct {
	revision                        int64
	action, id, name, query, before string
	item                            *SavedAgentSearch
}
type savedAgentSearchProblem string

func (e savedAgentSearchProblem) Error() string { return string(e) }

const (
	savedSearchInvalid          savedAgentSearchProblem = "invalid"
	savedSearchConflict         savedAgentSearchProblem = "conflict"
	savedSearchDuplicate        savedAgentSearchProblem = "duplicate"
	savedSearchLimit            savedAgentSearchProblem = "limit"
	savedSearchMissing          savedAgentSearchProblem = "missing"
	savedSearchStoreUnavailable savedAgentSearchProblem = "store"
	savedSearchUnsupported      savedAgentSearchProblem = "unsupported"
)

func validSavedSearchID(id string) bool {
	if len(id) != len("search_")+24 || !strings.HasPrefix(id, "search_") {
		return false
	}
	return strings.Trim(id[len("search_"):], "0123456789abcdef") == ""
}
func savedSearchText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}
func normalizeSavedSearch(name, query string) (string, string, error) {
	// ECMAScript String.trim (used before applying a saved palette query) also
	// strips BOM, while Go TrimSpace does not. Reject commands after that same
	// normalization so a saved literal cannot become a command when reopened.
	trim := func(value string) string {
		return strings.TrimFunc(value, func(r rune) bool { return r == '\uFEFF' || unicode.IsSpace(r) && r != '\u0085' })
	}
	name, query = trim(name), trim(query)
	if name == "" || utf8.RuneCountInString(name) > savedAgentSearchNameMax || !savedSearchText(name) || query == "" || len(query) > savedAgentSearchQueryMax || !savedSearchText(query) || strings.HasPrefix(query, ">") {
		return "", "", savedSearchInvalid
	}
	return name, query, nil
}
func validSavedSearchItem(item SavedAgentSearch) bool {
	name, query, err := normalizeSavedSearch(item.Name, item.Query)
	return err == nil && name == item.Name && query == item.Query && validSavedSearchID(item.ID) && !item.CreatedAt.IsZero() && !item.UpdatedAt.IsZero() && !item.UpdatedAt.Before(item.CreatedAt)
}

// savedSearchObject rejects duplicate, differently cased, unknown and trailing
// members instead of accepting the last spelling of an ambiguous mutation.
func savedSearchObject(raw []byte, allowed ...string) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, savedSearchInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, savedSearchInvalid
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || !slices.Contains(allowed, key) {
			return nil, savedSearchInvalid
		}
		if _, exists := fields[key]; exists {
			return nil, savedSearchInvalid
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, savedSearchInvalid
		}
		fields[key] = value
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
		return nil, savedSearchInvalid
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return nil, savedSearchInvalid
	}
	return fields, nil
}
func savedSearchString(fields map[string]json.RawMessage, key string, required bool) (string, error) {
	raw, exists := fields[key]
	if !exists && !required {
		return "", nil
	}
	var value string
	if !exists || string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
		return "", savedSearchInvalid
	}
	return value, nil
}
func parseSavedSearchMutation(raw []byte) (savedAgentSearchMutation, error) {
	fields, err := savedSearchObject(raw, "revision", "action", "id", "name", "query", "before_id", "item")
	if err != nil {
		return savedAgentSearchMutation{}, err
	}
	revision, err := savedSearchString(fields, "revision", true)
	if err != nil || revision == "" || len(revision) > 19 || strings.Trim(revision, "0123456789") != "" || len(revision) > 1 && revision[0] == '0' {
		return savedAgentSearchMutation{}, savedSearchInvalid
	}
	n, err := strconv.ParseInt(revision, 10, 64)
	if err != nil || n == math.MaxInt64 {
		return savedAgentSearchMutation{}, savedSearchInvalid
	}
	action, err := savedSearchString(fields, "action", true)
	if err != nil {
		return savedAgentSearchMutation{}, err
	}
	keys := []string{"revision", "action"}
	switch action {
	case "add":
		keys = append(keys, "name", "query")
	case "update":
		keys = append(keys, "id", "name", "query")
	case "delete":
		keys = append(keys, "id")
	case "move":
		keys = append(keys, "id", "before_id")
	case "restore":
		keys = append(keys, "item", "before_id")
	default:
		return savedAgentSearchMutation{}, savedSearchInvalid
	}
	for key := range fields {
		if !slices.Contains(keys, key) {
			return savedAgentSearchMutation{}, savedSearchInvalid
		}
	}
	m := savedAgentSearchMutation{revision: n, action: action}
	if action == "update" || action == "delete" || action == "move" {
		m.id, err = savedSearchString(fields, "id", true)
		if err != nil || !validSavedSearchID(m.id) {
			return m, savedSearchInvalid
		}
	}
	if action == "add" || action == "update" {
		m.name, err = savedSearchString(fields, "name", true)
		if err != nil {
			return m, err
		}
		m.query, err = savedSearchString(fields, "query", true)
		if err != nil {
			return m, err
		}
		m.name, m.query, err = normalizeSavedSearch(m.name, m.query)
		if err != nil {
			return m, err
		}
	}
	if action == "move" || action == "restore" {
		m.before, err = savedSearchString(fields, "before_id", false)
		if err != nil || m.before != "" && !validSavedSearchID(m.before) {
			return m, savedSearchInvalid
		}
	}
	if action == "restore" {
		raw, exists := fields["item"]
		if !exists {
			return m, savedSearchInvalid
		}
		if _, err := savedSearchObject(raw, "id", "name", "query", "created_at", "updated_at"); err != nil {
			return m, err
		}
		var item SavedAgentSearch
		if json.Unmarshal(raw, &item) != nil || !validSavedSearchItem(item) {
			return m, savedSearchInvalid
		}
		m.item = &item
	}
	return m, nil
}

func (s *Service) savedSearchStore() (savedAgentSearchStore, error) {
	st, ok := s.store().(savedAgentSearchStore)
	if !ok || st == nil {
		return nil, savedSearchUnsupported
	}
	return st, nil
}
func readSavedSearches(ctx context.Context, st savedAgentSearchStore) (savedAgentSearchDocument, error) {
	doc := savedAgentSearchDocument{Searches: []SavedAgentSearch{}}
	var raw json.RawMessage
	exists, err := st.Setting(ctx, savedAgentSearchesKey, &raw)
	if err != nil {
		return doc, savedSearchStoreUnavailable
	}
	if !exists {
		return savedAgentSearchDocument{Searches: []SavedAgentSearch{}}, nil
	}
	fields, err := savedSearchObject(raw, "revision", "searches")
	if err != nil || fields["revision"] == nil || fields["searches"] == nil || string(fields["revision"]) == "null" || string(fields["searches"]) == "null" || json.Unmarshal(raw, &doc) != nil {
		return doc, savedSearchStoreUnavailable
	}
	if doc.Revision < 0 || len(doc.Searches) > savedAgentSearchesMax {
		return doc, savedSearchStoreUnavailable
	}
	ids := map[string]bool{}
	for i, item := range doc.Searches {
		if !validSavedSearchItem(item) || ids[item.ID] {
			return doc, savedSearchStoreUnavailable
		}
		ids[item.ID] = true
		for _, other := range doc.Searches[:i] {
			if strings.EqualFold(item.Name, other.Name) {
				return doc, savedSearchStoreUnavailable
			}
		}
	}
	if doc.Searches == nil {
		doc.Searches = []SavedAgentSearch{}
	}
	return doc, nil
}
func listedSavedSearches(doc savedAgentSearchDocument, write bool) savedAgentSearchResponse {
	return savedAgentSearchResponse{Revision: strconv.FormatInt(doc.Revision, 10), Searches: doc.Searches, CanWrite: write}
}
func applySavedSearch(before savedAgentSearchDocument, m savedAgentSearchMutation, now time.Time) (savedAgentSearchDocument, error) {
	if before.Revision != m.revision {
		return savedAgentSearchDocument{}, savedSearchConflict
	}
	if before.Revision == math.MaxInt64 {
		return savedAgentSearchDocument{}, savedSearchInvalid
	}
	next := savedAgentSearchDocument{Revision: before.Revision + 1, Searches: append([]SavedAgentSearch{}, before.Searches...)}
	index := func(id string) int {
		return slices.IndexFunc(next.Searches, func(item SavedAgentSearch) bool { return item.ID == id })
	}
	duplicate := func(id, name string) bool {
		return slices.ContainsFunc(next.Searches, func(item SavedAgentSearch) bool { return item.ID != id && strings.EqualFold(item.Name, name) })
	}
	switch m.action {
	case "add":
		if len(next.Searches) >= savedAgentSearchesMax {
			return next, savedSearchLimit
		}
		if duplicate("", m.name) {
			return next, savedSearchDuplicate
		}
		id := "search_" + randomHex(12)
		for index(id) >= 0 {
			id = "search_" + randomHex(12)
		}
		next.Searches = append(next.Searches, SavedAgentSearch{ID: id, Name: m.name, Query: m.query, CreatedAt: now.UTC(), UpdatedAt: now.UTC()})
	case "update":
		i := index(m.id)
		if i < 0 {
			return next, savedSearchMissing
		}
		if duplicate(m.id, m.name) {
			return next, savedSearchDuplicate
		}
		next.Searches[i].Name, next.Searches[i].Query = m.name, m.query
		next.Searches[i].UpdatedAt = now.UTC()
		if next.Searches[i].UpdatedAt.Before(next.Searches[i].CreatedAt) {
			next.Searches[i].UpdatedAt = next.Searches[i].CreatedAt
		}
	case "delete":
		i := index(m.id)
		if i < 0 {
			return next, savedSearchMissing
		}
		next.Searches = slices.Delete(next.Searches, i, i+1)
	case "move", "restore":
		var item SavedAgentSearch
		if m.action == "move" {
			i := index(m.id)
			if i < 0 {
				return next, savedSearchMissing
			}
			if m.before == m.id {
				return next, savedSearchInvalid
			}
			item = next.Searches[i]
			next.Searches = slices.Delete(next.Searches, i, i+1)
		} else {
			if m.item == nil || !validSavedSearchItem(*m.item) {
				return next, savedSearchInvalid
			}
			item = *m.item
			if index(item.ID) >= 0 || duplicate("", item.Name) {
				return next, savedSearchDuplicate
			}
			if len(next.Searches) >= savedAgentSearchesMax {
				return next, savedSearchLimit
			}
		}
		at := len(next.Searches)
		if m.before != "" {
			at = index(m.before)
			if at < 0 {
				return next, savedSearchInvalid
			}
		}
		next.Searches = slices.Insert(next.Searches, at, item)
	default:
		return next, savedSearchInvalid
	}
	return next, nil
}
func (s *Service) changeSavedSearch(ctx context.Context, st savedAgentSearchStore, m savedAgentSearchMutation) (savedAgentSearchDocument, error) {
	before, err := readSavedSearches(ctx, st)
	if err != nil {
		return before, err
	}
	next, err := applySavedSearch(before, m, time.Now())
	if err != nil {
		return before, err
	}
	saved, err := st.CompareSetting(ctx, savedAgentSearchesKey, before.Revision, next)
	if err != nil {
		return before, savedSearchStoreUnavailable
	}
	if !saved {
		return before, savedSearchConflict
	}
	s.event(ctx, map[string]any{"type": "saved_agent_searches", "revision": strconv.FormatInt(next.Revision, 10)})
	return next, nil
}
func writeSavedSearchError(w http.ResponseWriter, err error) {
	status, code, key := http.StatusServiceUnavailable, "storage_unavailable", "storage"
	switch {
	case errors.Is(err, savedSearchUnsupported):
		code, key = "not_supported", "unavailable"
	case errors.Is(err, savedSearchInvalid):
		status, code, key = http.StatusBadRequest, "invalid_request", "invalid"
	case errors.Is(err, savedSearchConflict):
		status, code, key = http.StatusConflict, "conflict", "conflict"
	case errors.Is(err, savedSearchDuplicate):
		status, code, key = http.StatusConflict, "duplicate_name", "duplicate"
	case errors.Is(err, savedSearchLimit):
		status, code, key = http.StatusBadRequest, "limit_reached", "limit"
	case errors.Is(err, savedSearchMissing):
		status, code, key = http.StatusNotFound, "search_not_found", "missing"
	}
	apierr.WriteCode(w, status, code, i18n.Err("assistant-saved-agent-searches."+key))
}
func (s *Service) savedAgentSearchRoutes() {
	s.handle("GET /api/v1/saved-agent-searches", func(w http.ResponseWriter, r *http.Request) {
		st, err := s.savedSearchStore()
		if err != nil {
			writeSavedSearchError(w, err)
			return
		}
		doc, err := readSavedSearches(r.Context(), st)
		if err != nil {
			writeSavedSearchError(w, err)
			return
		}
		apierr.WriteJSON(w, listedSavedSearches(doc, fullRole(r)))
	})
	s.handle("POST /api/v1/saved-agent-searches", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.WriteCode(w, http.StatusForbidden, "forbidden", i18n.Err("assistant-saved-agent-searches.read_only"))
			return
		}
		st, err := s.savedSearchStore()
		if err != nil {
			writeSavedSearchError(w, err)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
		if err != nil {
			writeBodyErr(w, err, i18n.Err("assistant-saved-agent-searches.invalid"))
			return
		}
		mutation, err := parseSavedSearchMutation(raw)
		if err != nil {
			writeSavedSearchError(w, err)
			return
		}
		doc, err := s.changeSavedSearch(r.Context(), st, mutation)
		if err != nil {
			writeSavedSearchError(w, err)
			return
		}
		apierr.WriteJSON(w, listedSavedSearches(doc, true))
	})
}
