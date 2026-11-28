package kb

// The distill step of the knowledge points curation (topics.go): a project's new timeline lines, or a chat's changed
// summary, go to the model, which answers with candidate points, each filed under a topic (an existing one by name, or
// a new one) and a subtopic path. The candidates wait in the topic's points.json until the merge step folds them in
// (topic_merge.go). How far a source was distilled is its mark, .topics.json in its directory (bookkeeping, not
// searched): a project's count of timeline lines, a chat's summary hash. The first pass over existing timelines is the
// same step from line one, a few calls a round.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/prompts"
)

const (
	topicMarkFile = ".topics.json"
	// distillInput: bytes of timeline lines (or of a chat's summary) per call, each line clipped to distillLineRunes.
	// Kept small so a call's answer (at most distillMaxPoints points) comes back well inside its deadline also from a
	// model that writes 30–40 tokens a second (#4350: 12000 bytes and 25 points ran out of 3 minutes).
	distillInput     = 6000
	distillLineRunes = 600
	// distillMinLines: the fewest timeline lines a call that follows one that ran out of time carries (distillChunk).
	distillMinLines = 2
	// A project is due once it has distillBatch new lines, or fewer and its timeline has been quiet distillQuiet.
	distillBatch = 8
	distillQuiet = 30 * time.Minute
	// distillChatEvery: a chat whose summary changed is distilled again at most this often (compression rewrites the
	// summary with every batch).
	distillChatEvery    = 12 * time.Hour
	distillMaxPoints    = 12
	distillMaxNewTopics = 4   // new topics one call may open; the rest of its points for new topics are dropped
	maxTopics           = 150 // no new topic past this many
	pendingMax          = 200 // candidates a topic holds at most; the oldest go first
	// topicModelTimeout bounds one kb.topics model call, source distill and topic merge alike. Both call JSONMax with a
	// 12000 output ceiling that doubles to 24000 for its one truncated-reply retry, and that retry shares this same
	// context, so the old 3 minutes cut long runs mid-stream ("ai: read stream: context deadline exceeded") and parked
	// the source/topic in the retry backoff. Topic work runs in background worker lanes, so the longer budget is cheap;
	// set it back to 3*time.Minute to roll back (the topic timeout test pins the six minute value).
	topicModelTimeout = 6 * time.Minute
)

// topicMark is how far a source was distilled.
type topicMark struct {
	Lines   int       `json:"lines,omitempty"`   // a project: timeline lines distilled
	Size    int64     `json:"size,omitempty"`    // a project: log.md's size then (unchanged: nothing new, not read again)
	Summary string    `json:"summary,omitempty"` // a chat: the summary distilled (sha256, hex)
	At      time.Time `json:"at"`
}

// topicPath is a point's subtopic path as the model writes it: a list, or one string with " / " between the names.
type topicPath []string

func (p *topicPath) UnmarshalJSON(raw []byte) error {
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		*p = list
		return nil
	}
	var one string
	if err := json.Unmarshal(raw, &one); err != nil {
		*p = nil // anything else: no path, the point stays
		return nil
	}
	*p = nil
	for _, x := range strings.Split(one, "/") {
		if x = strings.TrimSpace(x); x != "" {
			*p = append(*p, x)
		}
	}
	return nil
}

type distillPoint struct {
	Topic string    `json:"topic"`
	Path  topicPath `json:"path"`
	Kind  string    `json:"kind"`
	Text  string    `json:"text"`
	Why   string    `json:"why"`
	When  string    `json:"when"`
}

type distillDigest struct {
	Points []distillPoint `json:"points"`
}

// distillSource is a project or chat with something to distill.
type distillSource struct {
	kind, id  string
	relevance int
}

// topicsOn: the curation runs (assistant.kb_topics, on unless turned off).
func (e *Engine) topicsOn() bool { return e.TopicsOn == nil || e.TopicsOn() }

func (s *Store) topicMark(kind, id string) topicMark {
	var m topicMark
	_, _ = readJSON(s.path(kind, id, topicMarkFile), &m)
	return m
}

func (s *Store) putTopicMark(kind, id string, m topicMark) error {
	return writeJSON(s.path(kind, id, topicMarkFile), m)
}

// timeline is a project's timeline lines, oldest first.
func (s *Store) timeline(slug string) []string {
	var lines []string
	s.scanLog(slug, func(l []byte) bool {
		lines = append(lines, string(l))
		return true
	})
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines
}

func summaryHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// dueForDistill are the sources with something new, most relevant first.
func (e *Engine) dueForDistill(now time.Time) []distillSource {
	var out []distillSource
	for _, p := range e.Store.Projects() {
		if !p.Live() || p.Status == "ignored" {
			continue
		}
		mark := e.Store.topicMark(KindProject, p.Slug)
		info, err := os.Stat(e.Store.path(KindProject, p.Slug, "log.md"))
		if err != nil || (mark.Size > 0 && info.Size() == mark.Size) {
			continue
		}
		fresh := len(e.Store.timeline(p.Slug)) - mark.Lines
		if fresh <= 0 {
			continue
		}
		quiet := now.Sub(info.ModTime()) >= distillQuiet
		if fresh >= distillBatch || quiet || mark.At.IsZero() {
			out = append(out, distillSource{KindProject, p.Slug, p.Relevance})
		}
	}
	for _, c := range e.Store.Chats() {
		if c.Importance == "P3" || !HasPeople(c) {
			continue // notifications and bots hold no knowledge of the owner's
		}
		sum := strings.TrimSpace(e.Store.Summary(KindChat, c.ID))
		if sum == "" {
			continue
		}
		mark := e.Store.topicMark(KindChat, c.ID)
		if mark.Summary == summaryHash(sum) || (!mark.At.IsZero() && now.Sub(mark.At) < distillChatEvery) {
			continue
		}
		out = append(out, distillSource{KindChat, c.ID, c.Relevance})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].relevance > out[j].relevance })
	return out
}

// topicWorkers is how many calls of a knowledge points step (distill, merge) run at once (#4247): the sources and
// topics of one run are independent, and the writes go through topicMu with the merge's fingerprint check. The
// connection gate (ai/limit.go) still caps what reaches the upstream.
const topicWorkers = 2

// DistillTopics distills at most max sources, topicWorkers at a time; how many calls it made, failed ones included. A
// source whose call failed (or panicked) waits like a project whose rewrite failed (retryWait); a paused connection or
// no model starts no more. Only the knowledge base loop calls it.
func (e *Engine) DistillTopics(ctx context.Context, max int) int {
	if e.Model == nil || !e.topicsOn() || e.Store.ReadOnly() {
		return 0
	}
	now := time.Now()
	var todo []distillSource
	for _, src := range e.dueForDistill(now) {
		if len(todo) >= max {
			break
		}
		if !e.retries().topics.waits(src.kind+"/"+src.id, "", now) {
			todo = append(todo, src)
		}
	}
	var calls atomic.Int32
	e.bounded(ctx, len(todo), topicWorkers, func(i int) bool {
		key := todo[i].kind + "/" + todo[i].id
		sent, err := e.distill(ctx, todo[i], now, e.retries().topics.chunk(key))
		if err != nil {
			if deferred(ctx, err) {
				return false
			}
			e.topicRetryLater(key, err, "distill")
			if distillTooBig(err) && sent > 0 {
				// The same stretch would run out again: the next try carries half of it (distillMinLines at least).
				e.retries().topics.shrink(key, halfStretch(sent))
			}
			calls.Add(1) // a failed call is a call
			return true
		}
		e.retries().topics.clear(key)
		calls.Add(1)
		return true
	}, func(i int, p any) {
		e.topicRetryLater(todo[i].kind+"/"+todo[i].id, fmt.Errorf("panic: %v", p), "distill")
	})
	return int(calls.Load())
}

func (e *Engine) topicRetryLater(key string, err error, step string) {
	r := e.retries().topics.fail(key, "")
	e.logf("topics: %s %s: %v (failed %d time(s) in a row, next try in %s)", step, key, err, r.failures, retryWait(r.failures))
}

// bounded runs job(i) for every i below n, at most workers at once, and starts no more once a job returns false (the
// connection is paused, no model, the run ended) or ctx ends. A job that panics is logged with its stack, handed to
// panicked (to put that source off: the same panic would otherwise come first every run) and stops the rest like a
// false, so one bad source cannot end the process (the loop's own recovery is in another goroutine).
func (e *Engine) bounded(ctx context.Context, n, workers int, job func(i int) bool, panicked func(i int, p any)) {
	var (
		wg   sync.WaitGroup
		stop atomic.Bool
		sem  = make(chan struct{}, max(1, workers))
	)
	for i := range n {
		if stop.Load() || ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		if stop.Load() || ctx.Err() != nil { // it may have changed while waiting for a slot
			<-sem
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if p := recover(); p != nil {
					e.logf("panic: %v\n%s", p, debug.Stack())
					stop.Store(true)
					panicked(i, p)
				}
			}()
			if !job(i) {
				stop.Store(true)
			}
		}()
	}
	wg.Wait()
}

// halfStretch is how many lines the call after one of sent lines that ran out of time carries.
func halfStretch(sent int) int { return max(distillMinLines, sent/2) }

// distillTooBig: the call ran out of time, or its answer did not fit even in twice the room: less input per call is
// what helps (a call that failed otherwise is tried again with the same stretch).
func distillTooBig(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ai.ErrTruncated)
}

// errSourceGone: the source went (forgotten, merged) while it was being distilled.
var errSourceGone = errors.New("the source is gone")

// distill makes one distill call for src and files its candidates; sent is how many timeline lines the call carried
// (a project's; 0 for a chat). maxLines > 0 caps them: a stretch that ran out of time before is tried in a smaller one
// (retrySet.shrink). The mark moves only over what was distilled.
func (e *Engine) distill(ctx context.Context, src distillSource, now time.Time, maxLines int) (sent int, err error) {
	in := prompts.KBTopicDistillInput{Topics: e.topicRefs()}
	mark := e.Store.topicMark(src.kind, src.id)
	switch src.kind {
	case KindProject:
		p, ok := e.Store.Project(src.id)
		if !ok {
			return 0, nil
		}
		lines := e.Store.timeline(src.id)
		from := min(mark.Lines, len(lines))
		size := 0
		to := from
		for to < len(lines) && (maxLines <= 0 || to-from < maxLines) {
			l := clipRunes(lines[to], distillLineRunes)
			if size > 0 && size+len(l) > distillInput {
				break
			}
			in.Lines = append(in.Lines, l)
			size += len(l)
			to++
		}
		in.Project, in.Name, in.Goal = true, p.Name, p.Goal
		in.From, in.To, in.Total = from+1, to, len(lines)
		sent = to - from
		mark = topicMark{Lines: to, At: now}
		if info, err := os.Stat(e.Store.path(KindProject, src.id, "log.md")); err == nil && to == len(lines) {
			mark.Size = info.Size() // read to the end: unchanged from here means nothing new
		}
	case KindChat:
		c, ok := e.Store.Chat(src.id)
		if !ok {
			return 0, nil
		}
		// A chat's summary is one piece (compression keeps it short): clipped to the same size, not split.
		sum := strings.TrimSpace(e.Store.Summary(KindChat, src.id))
		in.Name, in.Summary = c.Name, clip(sum, distillInput)
		mark = topicMark{Summary: summaryHash(sum), At: now}
	}
	owner := e.knowledgeOwner()
	in.Language = owner.Language
	var d distillDigest
	cctx, cancel := context.WithTimeout(ctx, topicModelTimeout)
	defer cancel()
	system := prompts.RenderKBTopicDistill(prompts.KBTopicDistill{Language: owner.Language, Owner: e.ownerName(), MaxPoints: distillMaxPoints})
	if err := e.modelFor("kb.topics").JSONMax(cctx, system, prompts.RenderKBTopicDistillInput(in), 12000, &d); err != nil {
		return sent, err
	}
	return sent, e.fileCandidates(src, d.Points, mark, now)
}

// topicRefs are the existing topics the model may file points under, the biggest first.
func (e *Engine) topicRefs() []prompts.KBTopicRef {
	topics := e.Store.Topics()
	sort.SliceStable(topics, func(i, j int) bool { return topics[i].Points > topics[j].Points })
	var out []prompts.KBTopicRef
	for _, t := range topics {
		if len(out) == 80 {
			break
		}
		out = append(out, prompts.KBTopicRef{Name: t.Name, Aliases: t.Aliases, Note: t.Note})
	}
	return out
}

// fileCandidates files a source's candidate points under their topics, then moves the source's mark: a crash between
// the two only files the same candidates again, which the merge folds into the points they match.
func (e *Engine) fileCandidates(src distillSource, points []distillPoint, mark topicMark, now time.Time) error {
	s := e.Store
	s.topicMu.Lock()
	defer s.topicMu.Unlock()
	if !s.sourceExists(src) {
		return errSourceGone
	}
	day := now.In(s.Zone()).Format("2006-01-02")
	topics := s.Topics()
	opened, dropped := 0, 0
	filed := map[string][]TopicPoint{}
	byslug := map[string]*Topic{}
	var order []string
	for i, raw := range points {
		if i >= distillMaxPoints {
			break
		}
		p, ok := cleanTopicPoint(TopicPoint{Path: raw.Path, Kind: raw.Kind, Text: raw.Text, Why: raw.Why, When: raw.When})
		name := clipRunes(raw.Topic, topicPathRunes)
		if !ok || name == "" {
			continue
		}
		t := s.findTopic(name, topics)
		if t == nil {
			if opened >= distillMaxNewTopics || len(topics) >= maxTopics {
				dropped++
				continue
			}
			slug := s.newTopicSlug(name)
			if slug == "" {
				continue
			}
			t = &Topic{Slug: slug, Name: name, Created: now.UTC()}
			topics = append(topics, t)
			opened++
		}
		if p.When == "" {
			p.When = day
		}
		p.Sources = []TopicSource{{Kind: src.kind, ID: src.id, Day: p.When}}
		if _, ok := byslug[t.Slug]; !ok {
			order = append(order, t.Slug)
		}
		byslug[t.Slug] = t
		filed[t.Slug] = append(filed[t.Slug], p)
	}
	if dropped > 0 {
		e.logf("topics: %s/%s: %d point(s) for new topics dropped (at most %d new a call, %d in all)", src.kind, src.id, dropped, distillMaxNewTopics, maxTopics)
	}
	for _, slug := range order {
		t := byslug[slug]
		f, err := s.topicFile(slug)
		if err != nil {
			return err
		}
		f.Pending = append(f.Pending, filed[slug]...)
		if over := len(f.Pending) - pendingMax; over > 0 {
			f.Pending = f.Pending[over:]
		}
		if err := s.writeTopic(t, f, now.UTC()); err != nil {
			return err
		}
	}
	return s.putTopicMark(src.kind, src.id, mark)
}

func (s *Store) sourceExists(src distillSource) bool {
	switch src.kind {
	case KindProject:
		p, ok := s.Project(src.id)
		return ok && p.Live()
	case KindChat:
		_, ok := s.Chat(src.id)
		return ok
	}
	return false
}
