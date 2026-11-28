package kb

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/prompts"
)

// projectRetry is a project whose rewrite failed: how many times in a row, and when it may be tried again. Kept in the
// engine's memory, not in the project's file: PutProject would bump its Updated time and tell the clients about a
// change that is not one, and after a restart one more try is cheap.
type projectRetry struct {
	failures int
	at       time.Time
	// about is what failed (a recording's revision, an item's title and time): a source changed since is tried at
	// once, its failures forgotten. "" for steps without a cheap change signal (projects, topics' sources).
	about string
	// chunk: a knowledge points source whose distill call ran out of time: how many timeline lines its next call
	// carries at most (#4350); 0 for no cap.
	chunk int
}

// retrySet is a step's sources whose call failed, by key: the knowledge base's steps that call the model per source
// (projects, topics, recordings, items) put a failing one off this way (retryWait), so it costs a call now and then,
// not every round. Safe for concurrent use: the knowledge points lane runs two calls of a step at once (#4247). It
// lives on the Service's knowledge base state, not the engine, so a new engine (the hub's settings changed) keeps it.
type retrySet struct {
	mu sync.Mutex
	m  map[string]projectRetry
}

// waits: key failed about about and its next try is not due at now. One that changed since waits no more.
func (r *retrySet) waits(key, about string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.m[key]
	return ok && f.about == about && now.Before(f.at)
}

// fail counts one more failure in a row of key, about what it was asked about, and returns its new state: how many
// failures, and when it may be tried again. A failure about something else starts the count again.
func (r *retrySet) fail(key, about string) projectRetry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]projectRetry{}
	}
	f := r.m[key]
	if f.about != about {
		f = projectRetry{about: about}
	}
	f.failures++
	f.at = time.Now().Add(retryWait(f.failures))
	r.m[key] = f
	return f
}

// chunk is how many timeline lines key's next distill call carries at most (shrink); 0 for no cap.
func (r *retrySet) chunk(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.m[key].chunk
}

// shrink caps key's next distill call at lines (after fail: it keeps the failure's wait). clear forgets it.
func (r *retrySet) shrink(key string, lines int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.m[key]; ok {
		f.chunk = lines
		r.m[key] = f
	}
}

// clear forgets key's failures (it succeeded).
func (r *retrySet) clear(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, key)
}

// keep forgets the failures of every key not in due (done, merged or gone in the meantime).
func (r *retrySet) keep(due map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.m {
		if !due[key] {
			delete(r.m, key)
		}
	}
}

// deferred: err puts the work off rather than failing it, so nothing waits for it and the step stops: the connection is
// paused, no model is set up for it (the next round asks again, as before; not a source's fault), or the run ended.
func deferred(ctx context.Context, err error) bool {
	return ai.IsPaused(err) || ai.IsNotConfigured(err) || ctx.Err() != nil
}

type projectDigest struct {
	Summary    string     `json:"summary"`
	OneLine    string     `json:"one_line"`
	Goal       string     `json:"goal"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	Importance string     `json:"importance"`
	Relevance  modelScore `json:"relevance"`
	Tags       []string   `json:"tags"`
	Keywords   []string   `json:"keywords"`
}

// RefreshProjects rewrites the summaries of projects with news, at most max model calls per run, failed ones included
// (they cost a call as much as the others; a project gone meanwhile costs none); it returns how many it rewrote. A project whose rewrite failed waits like a chat
// whose compression failed (retryWait), so a model that keeps failing (2026-11-08: a model name the endpoint did not
// serve, 404 every two minutes) does not cost a call and a log line every round. Only the knowledge base loop calls it.
func (e *Engine) RefreshProjects(ctx context.Context, max int) int {
	if e.Model == nil {
		return 0
	}
	now := time.Now()
	var dirty []*Project
	live := map[string]bool{}
	for _, p := range e.Store.Projects() {
		if p.Dirty && p.Status != "ignored" && p.Live() && !p.referenceOnly() {
			live[p.Slug] = true
			if e.retries().projects.waits(p.Slug, "", now) {
				continue
			}
			dirty = append(dirty, p)
		}
	}
	e.retries().projects.keep(live) // done, merged or gone in the meantime
	sort.Slice(dirty, func(i, j int) bool { return dirty[i].Relevance > dirty[j].Relevance })
	n, calls := 0, 0
	for _, p := range dirty {
		if calls >= max || ctx.Err() != nil {
			break
		}
		called, err := e.refreshProjectCall(ctx, p)
		if called {
			calls++
		}
		if err != nil {
			if errors.Is(err, ErrReferenceOnly) {
				e.retries().projects.clear(p.Slug)
				e.logf("project %s: %v: skipped in-flight rewrite", p.Slug, err)
				continue
			}
			if deferred(ctx, err) { // the connection is held, no model, or the run ended: the rest wait (still dirty)
				break
			}
			r := e.retries().projects.fail(p.Slug, "")
			e.logf("project %s: %v (failed %d time(s) in a row, next try in %s)", p.Slug, err, r.failures, retryWait(r.failures))
			continue
		}
		e.retries().projects.clear(p.Slug)
		if called {
			n++
		}
	}
	return n
}

func (e *Engine) refreshProject(ctx context.Context, p *Project) error {
	_, err := e.refreshProjectCall(ctx, p)
	return err
}

// projectRefreshTimeout bounds one kb.project digest model call. Large projects ask for up to 8000 output tokens and
// often reach the ceiling, and JSONMax's larger retry shares this same context, so 3 minutes cut long replies mid-read
// ("ai: read stream: context deadline exceeded") and parked the project in the retry backoff. Project refreshes run
// serially in their own lane, so the longer background budget is cheap; set it back to 3*time.Minute to roll back.
const projectRefreshTimeout = 6 * time.Minute

// refreshProjectCall rewrites p's summary; called: the model was asked (not for a project gone meanwhile).
func (e *Engine) refreshProjectCall(ctx context.Context, p *Project) (called bool, err error) {
	current, ok := e.Store.Project(p.Slug)
	if !ok || current.referenceOnly() {
		return false, nil
	}
	p = current
	owner := e.knowledgeOwner()
	in := e.projectInput(p)
	in.Language = owner.Language
	var d projectDigest
	cctx, cancel := context.WithTimeout(ctx, projectRefreshTimeout)
	defer cancel()
	if err := e.modelFor("kb.project").JSONMax(cctx, prompts.RenderKBProject(owner), prompts.RenderKBProjectInput(in.KBProjectInput), 8000, &d); err != nil {
		return true, err
	}
	return true, e.commitProject(p, in, d)
}

func (e *Engine) projectInput(p *Project) projectInput {
	tags := e.TopTags(40)
	in, _ := e.projectEvidence(p, e.Store.projectTimeline(p, false))
	in.Tags = tags
	in.Summary = e.Store.Summary(KindProject, p.Slug)
	return in
}
