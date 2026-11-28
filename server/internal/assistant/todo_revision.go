package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
)

// The pushed web read first asks for this cheap validator (#13804). It hashes the write revision, stable view classes
// and list settings and labels. Formatting every row, its brief, lineage and runs just to answer 304 was the costly
// part of #9590's full-list ETag. The old GET /todos response is unchanged.
//
// A full read emits a token only when its write revision, classes and projection inputs still match the current
// token. A concurrent change can cost a later full read. The per-process instance prevents a restarted manager
// from reusing a token.
type todoRevisionBook struct {
	writes   atomic.Uint64
	once     sync.Once
	instance string
}

func (b *todoRevisionBook) changed() { b.writes.Add(1) }

func (b *todoRevisionBook) id() string {
	b.once.Do(func() { b.instance = randomHex(16) })
	return b.instance
}

// todoRevisionChanged invalidates a list and schedules its refresh, including changes outside the to-do rows.
func (s *Service) todoRevisionChanged() {
	s.trevision.changed()
	s.todosPushSoon()
}

// todoRevisionPrefs are the effective Labs settings that affect the list projection.
type todoRevisionPrefs struct {
	V2            bool
	FoldChildren  bool
	PendingReview int
	CanWait       int
}

func todoRevisionPrefsOf(v Settings) todoRevisionPrefs {
	return todoRevisionPrefs{
		V2: v.todosV2On(), FoldChildren: v.todosV2FoldChildrenOn(),
		PendingReview: v.todosV2PendingReviewSLAHours(), CanWait: v.todosV2CanWaitSLAHours(),
	}
}

// todoRevisionView is the comparable part of a row's view, with its id, in a JSON-encodable form.
type todoRevisionView struct {
	ID       string                      `json:"id"`
	Phase    string                      `json:"phase"`
	Section  string                      `json:"section"`
	Reason   string                      `json:"reason"`
	Asks     string                      `json:"asks,omitempty"`
	Inactive int64                       `json:"inactive,omitempty"`
	CanWait  bool                        `json:"can_wait,omitempty"`
	Seen     bool                        `json:"seen,omitempty"`
	Steps    assistantwire.TodoStepCount `json:"steps"`
}

func todoRevisionViewOf(id string, c todoClass) todoRevisionView {
	return todoRevisionView{ID: id, Phase: c.phase, Section: c.section, Reason: c.reason, Asks: c.asks,
		Inactive: c.inactive, CanWait: c.canWait, Seen: c.seen, Steps: c.steps}
}

func (s *Service) todoRevisionFor(writes uint64, views []todoRevisionView, known bool, prefs todoRevisionPrefs, labels map[string]string) string {
	slices.SortFunc(views, func(a, b todoRevisionView) int { return strings.Compare(a.ID, b.ID) })
	// JSON sorts map keys, so the fingerprint is stable across reads of the same direction labels.
	raw, _ := json.Marshal(struct {
		Instance string             `json:"instance"`
		Writes   uint64             `json:"writes"`
		Known    bool               `json:"known"`
		Views    []todoRevisionView `json:"views"`
		Prefs    todoRevisionPrefs  `json:"prefs"`
		Labels   map[string]string  `json:"labels"`
	}{Instance: s.trevision.id(), Writes: writes, Known: known, Views: views, Prefs: prefs, Labels: labels})
	sum := sha256.Sum256(raw)
	return `W/"tr1-` + hex.EncodeToString(sum[:16]) + `"`
}

// todoRevisionCurrent reads stable classes and the small set of settings and labels that can change a list
// independently of its rows. It never loads, formats or projects the to-do list.
func (s *Service) todoRevisionCurrent(ctx context.Context) string {
	b := &s.tview
	b.mu.Lock()
	views := make([]todoRevisionView, 0, len(b.listed))
	for id, class := range b.listed {
		views = append(views, todoRevisionViewOf(id, class))
	}
	known := b.listed != nil
	b.mu.Unlock()
	cfg, _ := s.settingsLastGood(ctx)
	labels := s.todoDirectionLabels(ctx, cfg)
	return s.todoRevisionFor(s.trevision.writes.Load(), views, known, todoRevisionPrefsOf(cfg), labels)
}

// tagTodoRevision attaches a token for the full answer only if it also describes the current list.
func (s *Service) tagTodoRevision(ctx context.Context, w http.ResponseWriter, writes uint64, p todoProjection, prefs todoRevisionPrefs, labels map[string]string) {
	if writes != s.trevision.writes.Load() {
		return
	}
	views := make([]todoRevisionView, 0, len(p.facts))
	for i := range p.facts {
		views = append(views, todoRevisionViewOf(p.facts[i].ID, todoClassOf(p.views[i])))
	}
	tag := s.todoRevisionFor(writes, views, true, prefs, labels)
	if tag == s.todoRevisionCurrent(ctx) {
		w.Header().Set(assistantwire.TodoRevisionHeader, tag)
	}
}

func todoRevisionMatches(lines []string, tag string) bool {
	for _, line := range lines {
		for _, candidate := range strings.Split(line, ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate == "*" || candidate == tag {
				return true
			}
		}
	}
	return false
}

func (s *Service) todoRevisionRead(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(RoleHeader) == roleExtension {
		// extensionRows has its own scoped projection, which this internal validator does not describe.
		apierr.WriteCode(w, http.StatusConflict, "todo_revision_unavailable", errors.New("revision unavailable for extension rows"))
		return
	}
	tag := s.todoRevisionCurrent(r.Context())
	w.Header().Set("ETag", tag)
	w.Header().Set(assistantwire.TodoRevisionHeader, tag)
	if todoRevisionMatches(r.Header.Values("If-None-Match"), tag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	apierr.WriteJSON(w, struct {
		Revision string `json:"revision"`
	}{Revision: tag})
}
