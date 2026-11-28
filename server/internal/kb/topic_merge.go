package kb

// The merge step of the knowledge points curation (topics.go): a topic's waiting candidates are folded into its points.
// The model answers with edits against the existing points (add a point from some candidates, attach candidates to a
// point or reword it, drop a point), never the whole list, so its answer stays short however big the topic is. The
// engine checks every edit (known ids, kinds, lengths) and works out each point's sources and date from the
// candidates it names. Candidates no edit names are let go: the model judged them not worth keeping.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

const (
	mergeBatch = 40 // candidates per call
	// A topic past maxTopicPoints+mergeSlack points (the model was asked to keep under maxTopicPoints) loses its oldest.
	mergeSlack = 50
)

type mergeOp struct {
	Op   string          `json:"op"` // add | update | drop
	ID   string          `json:"id"`
	From []string        `json:"from"`
	Path json.RawMessage `json:"path"` // absent or null: unchanged (hasPath)
	Kind string          `json:"kind"`
	Text string          `json:"text"`
	Why  string          `json:"why"`
}

type mergeDigest struct {
	Note string    `json:"note"`
	Ops  []mergeOp `json:"ops"`
}

// RefreshTopics merges the candidates of at most max topics, those waiting longest first, topicWorkers at a time; how
// many merges it tried, failed ones included. A topic whose merge failed (or panicked) waits (retryWait) like a project
// whose rewrite failed; a paused connection or no model starts no more. Only the knowledge base loop calls it.
func (e *Engine) RefreshTopics(ctx context.Context, max int) int {
	if e.Model == nil || !e.topicsOn() || e.Store.ReadOnly() {
		return 0
	}
	now := time.Now()
	var dirty []*Topic
	for _, t := range e.Store.Topics() {
		if t.Dirty && !e.retries().topics.waits(KindTopic+"/"+t.Slug, "", now) {
			dirty = append(dirty, t)
		}
	}
	sort.SliceStable(dirty, func(i, j int) bool { return dirty[i].Updated.Before(dirty[j].Updated) })
	dirty = dirty[:min(len(dirty), max)]
	var tried atomic.Int32
	e.bounded(ctx, len(dirty), topicWorkers, func(i int) bool {
		key := KindTopic + "/" + dirty[i].Slug
		if err := e.mergeTopic(ctx, dirty[i].Slug); err != nil {
			if deferred(ctx, err) {
				return false
			}
			e.topicRetryLater(key, err, "merge")
			tried.Add(1)
			return true
		}
		e.retries().topics.clear(key)
		tried.Add(1)
		return true
	}, func(i int, p any) {
		e.topicRetryLater(KindTopic+"/"+dirty[i].Slug, fmt.Errorf("panic: %v", p), "merge")
	})
	return int(tried.Load())
}

// topicFingerprint is what a merge was asked about: the points and the batch of candidates. The answer is applied only
// while both are unchanged (a source forgotten meanwhile changes them; the merge runs again next round).
func topicFingerprint(points, batch []TopicPoint) []byte {
	raw, _ := json.Marshal([2][]TopicPoint{points, batch})
	return raw
}

func (e *Engine) mergeTopic(ctx context.Context, slug string) error {
	s := e.Store
	s.topicMu.Lock()
	t, ok := s.Topic(slug)
	f, err := s.topicFile(slug)
	s.topicMu.Unlock()
	if !ok || err != nil {
		return err
	}
	if len(f.Pending) == 0 {
		s.topicMu.Lock()
		defer s.topicMu.Unlock()
		if t, ok = s.Topic(slug); ok && t.Dirty {
			return s.writeTopic(t, f, time.Now().UTC())
		}
		return nil
	}
	batch := f.Pending[:min(len(f.Pending), mergeBatch)]
	asked := topicFingerprint(f.Points, batch)
	cands := make([]TopicPoint, len(batch))
	for i, c := range batch {
		c.ID = fmt.Sprintf("c%d", i+1)
		cands[i] = c
	}
	owner := e.knowledgeOwner()
	in := prompts.KBTopicMergeInput{Language: owner.Language, Name: t.Name, Aliases: t.Aliases, Note: t.Note, Pinned: t.Pinned, Points: f.Points, Candidates: cands}
	var d mergeDigest
	cctx, cancel := context.WithTimeout(ctx, topicModelTimeout)
	defer cancel()
	system := prompts.RenderKBTopicMerge(prompts.KBTopicMerge{Language: owner.Language, Owner: e.ownerName(), MaxPoints: maxTopicPoints})
	if err := e.modelFor("kb.topics").JSONMax(cctx, system, prompts.RenderKBTopicMergeInput(in), 12000, &d); err != nil {
		return err
	}
	s.topicMu.Lock()
	defer s.topicMu.Unlock()
	t, ok = s.Topic(slug)
	if !ok {
		return nil
	}
	f, err = s.topicFile(slug)
	if err != nil {
		return err
	}
	if len(f.Pending) < len(batch) || !bytes.Equal(topicFingerprint(f.Points, f.Pending[:len(batch)]), asked) {
		return nil // changed meanwhile: merged again next round
	}
	f.Points, f.Next = applyMergeOps(f.Points, cands, d.Ops, f.Next)
	f.Pending = f.Pending[len(batch):]
	if note := oneLine(d.Note); note != "" {
		t.Note = note // putEntity keeps a note the user wrote
	}
	now := time.Now().UTC()
	if err := s.writeTopic(t, f, now); err != nil {
		return err
	}
	if t, ok = s.Topic(slug); ok {
		e.index(KindTopic, t.Slug, t.Name, t.Tagging, t.Note, t.Updated)
	}
	return nil
}

// applyMergeOps applies the model's edits to points, cands being the candidates as numbered for it (c1…). next is the
// number the next new point gets (p<next>); it returns the points and the next number. Edits that name unknown points
// or no known candidate, or carry no valid kind and text, are skipped.
func applyMergeOps(points, cands []TopicPoint, ops []mergeOp, next int) ([]TopicPoint, int) {
	if next <= len(points) {
		next = len(points) + 1
		for _, p := range points { // ids from before: never reuse one
			var k int
			if _, err := fmt.Sscanf(p.ID, "p%d", &k); err == nil && k >= next {
				next = k + 1
			}
		}
	}
	out := slices.Clone(points)
	at := func(id string) int { return slices.IndexFunc(out, func(p TopicPoint) bool { return p.ID == id }) }
	from := func(ids []string) []TopicPoint {
		var got []TopicPoint
		for _, id := range ids {
			if i := slices.IndexFunc(cands, func(c TopicPoint) bool { return c.ID == id }); i >= 0 {
				got = append(got, cands[i])
			}
		}
		return got
	}
	attach := func(p *TopicPoint, cs []TopicPoint) {
		for _, c := range cs {
			p.Sources = mergeSources(p.Sources, c.Sources...)
			if c.When > p.When {
				p.When = c.When
			}
		}
	}
	dropped := map[string]bool{}
	for _, op := range ops {
		switch op.Op {
		case "add":
			cs := from(op.From)
			if len(cs) == 0 {
				continue // knowledge the candidates do not hold
			}
			p := TopicPoint{Path: cs[0].Path, Kind: cs[0].Kind, Text: cs[0].Text, Why: cs[0].Why}
			if hasPath(op.Path) {
				p.Path = decodePath(op.Path)
			}
			if op.Kind != "" {
				p.Kind = op.Kind
			}
			if op.Text != "" {
				p.Text, p.Why = op.Text, op.Why
			}
			p, ok := cleanTopicPoint(p)
			if !ok {
				continue
			}
			attach(&p, cs)
			p.ID = fmt.Sprintf("p%d", next)
			next++
			out = append(out, p)
		case "update":
			i := at(op.ID)
			if i < 0 || dropped[op.ID] {
				continue
			}
			p := out[i]
			if op.Kind != "" {
				p.Kind = op.Kind
			}
			if op.Text != "" {
				p.Text = op.Text
			}
			if op.Why != "" {
				p.Why = op.Why
			}
			if hasPath(op.Path) {
				p.Path = decodePath(op.Path)
			}
			cleaned, ok := cleanTopicPoint(p)
			if !ok {
				cleaned = out[i] // a bad rewording: keep the point as it was, still attach the candidates
			}
			cleaned.ID, cleaned.Sources, cleaned.When = out[i].ID, out[i].Sources, out[i].When
			attach(&cleaned, from(op.From))
			out[i] = cleaned
		case "drop":
			if at(op.ID) >= 0 {
				dropped[op.ID] = true
			}
		}
	}
	out = slices.DeleteFunc(out, func(p TopicPoint) bool { return dropped[p.ID] })
	if over := len(out) - (maxTopicPoints + mergeSlack); over > 0 {
		oldest := slices.Clone(out)
		sort.SliceStable(oldest, func(i, j int) bool { return oldest[i].When < oldest[j].When })
		gone := map[string]bool{}
		for _, p := range oldest[:over] {
			gone[p.ID] = true
		}
		out = slices.DeleteFunc(out, func(p TopicPoint) bool { return gone[p.ID] })
	}
	return out, next
}

// hasPath: the edit gives a path (absent or null leaves it as it was; [] moves the point to the topic itself).
func hasPath(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }

func decodePath(raw json.RawMessage) []string {
	var p topicPath
	_ = p.UnmarshalJSON(raw)
	return cleanTopicPath(p)
}
