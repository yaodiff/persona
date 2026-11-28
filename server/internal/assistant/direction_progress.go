package assistant

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// A direction's name and its leader's own progress (#3679; docs/assistant/unified-entry.md "Names and progress").
//
// A work-log extension shows each direction under its name (leader.changed name, directionName): the one the owner
// (PUT …/directions/{id}/name) or the secretary (direction_name) gave it, else its project's, else its title without
// the "MMDD | kind | " prefix. The leader writes its direction's summary itself, at the goal level (leader_progress):
// what was reached, what is being done and where it is stuck, what the owner has to do, with evidence links; it goes
// to the extensions as leader.report kind progress and the last one stays on the direction.

const (
	directionNameMost = 40  // characters
	progressPartMost  = 200 // characters of each part
	progressLinksMost = 5
)

// leaderProgress is a leader's summary of its direction.
type leaderProgress struct {
	Reached  string    `json:"reached,omitempty"`
	Doing    string    `json:"doing,omitempty"`
	NeedsYou string    `json:"needs_you,omitempty"`
	Links    []string  `json:"links,omitempty"`
	At       time.Time `json:"at"`
}

var errNoDirection = errors.New("no such direction: GET /api/v1/assistant/leaders lists them")

// setDirectionName names direction id (a leader now or before) as by chose; "" clears it (the fallbacks show again).
func (s *Service) setDirectionName(ctx context.Context, id, name, by string) error {
	name = clip(oneLine(name), directionNameMost)
	if name == "" {
		by = ""
	}
	b := &s.directions
	b.mu.Lock()
	if !s.directionsLocked(ctx) {
		b.mu.Unlock()
		return errors.New("the directions could not be read")
	}
	d := b.m[id]
	if d == nil {
		b.mu.Unlock()
		return errNoDirection
	}
	oldName, oldBy := d.Name, d.NameBy
	if name == oldName && by == oldBy {
		b.mu.Unlock()
		return nil
	}
	d.Name, d.NameBy = name, by
	if err := s.saveDirectionsCheckedLocked(ctx); err != nil {
		d.Name, d.NameBy = oldName, oldBy
		b.mu.Unlock()
		return err
	}
	b.mu.Unlock()
	if name != oldName {
		s.todoRevisionChanged()
	}
	s.publish(ctx) // session titles update now; the existing direction/name events follow
	return nil
}

// toolDirectionName is direction_name: the secretary names a direction.
func (s *Service) toolDirectionName(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var a struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	if !s.isMain(ctx, self) {
		return "", errors.New("only the main conversation names directions")
	}
	if err := s.setDirectionName(ctx, strings.TrimSpace(a.ID), a.Name, directionByAssistant); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Name) == "" {
		return fmt.Sprintf("Direction %s has no name of its own now: it shows its project's name, else its title.", a.ID), nil
	}
	return fmt.Sprintf("Direction %s is called “%s” now (the app and the work-log extension show it).", a.ID, clip(oneLine(a.Name), directionNameMost)), nil
}

// normalizeLeaderProgress applies the same normalization to MCP, HTTP and node CLI reports.
func normalizeLeaderProgress(a directionProgressInput, at time.Time) (leaderProgress, error) {
	p := leaderProgress{Reached: clip(oneLine(a.Reached), progressPartMost), Doing: clip(oneLine(a.Doing), progressPartMost),
		NeedsYou: clip(oneLine(a.NeedsYou), progressPartMost), At: at}
	for _, l := range a.Links {
		if l = strings.TrimSpace(l); l != "" && !slices.Contains(p.Links, l) && len(p.Links) < progressLinksMost {
			p.Links = append(p.Links, clip(l, 500))
		}
	}
	if p.Reached == "" && p.Doing == "" && p.NeedsYou == "" {
		return leaderProgress{}, errors.New("say at least one of reached, doing and needs_you")
	}
	return p, nil
}

// toolLeaderProgress is leader_progress: a leader reports its direction's progress at the goal level.
func (s *Service) toolLeaderProgress(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var a directionProgressInput
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	if !s.isLeader(ctx, self) {
		return "", errors.New("only a leader conversation (assistant.leaders) reports its direction's progress: tell the user in your reply instead")
	}
	p, err := normalizeLeaderProgress(a, time.Now().UTC())
	if err != nil {
		return "", err
	}
	told, err := s.leaderProgressReport(ctx, self, p)
	if err != nil {
		return "", err
	}
	if !told {
		return "Kept as the direction's latest progress (this hub tells no extensions).", nil
	}
	return "Reported as the direction's latest progress: an extension that keeps the work log (if one is installed) shows it.", nil
}

// leaderProgressReport keeps p as leader lead's latest and tells the extensions that subscribe (false: this hub tells
// none).
func (s *Service) leaderProgressReport(ctx context.Context, lead string, p leaderProgress) (bool, error) {
	b := &s.directions
	b.mu.Lock()
	if !s.directionsLocked(ctx) {
		b.mu.Unlock()
		return false, errors.New("the directions could not be read")
	}
	d := b.m[lead]
	created := d == nil
	if created { // a leader the sync has not recorded yet
		d = &direction{MachineID: s.settings(ctx).MachineID, By: directionByAssistant, At: p.At}
		b.m[lead] = d
	}
	previous := d.Progress
	cp := p
	d.Progress = &cp
	if err := s.saveDirectionsCheckedLocked(ctx); err != nil {
		if created {
			delete(b.m, lead)
		} else {
			d.Progress = previous
		}
		b.mu.Unlock()
		return false, err
	}
	b.mu.Unlock()
	if !s.todoEventsOn() {
		return false, nil
	}
	l := s.lang(ctx)
	var parts []string
	for _, x := range []struct{ key, text string }{{"progress_reached", p.Reached}, {"progress_doing", p.Doing}, {"progress_needs_you", p.NeedsYou}} {
		if x.text != "" {
			parts = append(parts, i18n.T(l, "assistant-reports."+x.key, i18n.Args{"text": x.text}))
		}
	}
	key := "progress|" + lead + "|" + p.At.Format(time.RFC3339Nano)
	sum := sha256.Sum256([]byte(key))
	ev := assistantwire.LeaderReportEvent{ID: "lrep_" + hex.EncodeToString(sum[:8]), Leader: lead, Kind: assistantwire.LeaderProgress,
		Text: clip(strings.Join(parts, "\n"), assistantwire.LeaderReportTextMost), At: p.At, Reached: p.Reached, Doing: p.Doing,
		NeedsYou: p.NeedsYou, Links: p.Links, Level: s.leaderReportLevel(ctx, assistantwire.LeaderProgress)}
	if len(p.Links) > 0 {
		ev.Link = p.Links[0]
	}
	s.todoQueue(todoNotice{assistantwire.EventLeaderReport, ev})
	return true, nil
}

// directionNameRoutes: the owner names a direction.
func (s *Service) directionNameRoutes() {
	s.handle("PUT /api/v1/assistant/directions/{id}/name", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change directions"))
			return
		}
		var in struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		err := s.setDirectionName(r.Context(), r.PathValue("id"), in.Name, directionByUser)
		switch {
		case errors.Is(err, errNoDirection):
			apierr.Write(w, http.StatusNotFound, err)
			return
		case err != nil:
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"name": clip(oneLine(in.Name), directionNameMost)})
	})
}

// directionOf is a copy of direction id's record, nil when it has none or the records cannot be read.
func (s *Service) directionOf(id string) *direction {
	b := &s.directions
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.directionsLocked(context.Background()) || b.m[id] == nil {
		return nil
	}
	cp := *b.m[id]
	return &cp
}

// directionSessionNames uses the same canonical resolver as the tree without opening
// the knowledge base while publish holds pubMu. directionsSync publishes refreshed
// project fallbacks once the knowledge base is available; explicit names are immediate.
func (s *Service) directionSessionNames(ctx context.Context, cfg Settings, titles map[string]shownTitle) map[string]string {
	return s.directionNames(ctx, cfg, titles, false)
}

// todoDirectionLabels includes recorded directions even after they stop receiving new work.
func (s *Service) todoDirectionLabels(ctx context.Context, cfg Settings) map[string]string {
	titles, _ := s.conversationTitles()
	return s.directionNames(ctx, cfg, titles, true)
}

// directionNames can include ended directions for to-do attribution. Only cfg.Leaders are current leaders.
func (s *Service) directionNames(ctx context.Context, cfg Settings, titles map[string]shownTitle, includeEnded bool) map[string]string {
	b := &s.directions
	b.mu.Lock()
	loaded := s.directionsLocked(ctx)
	if !loaded && !includeEnded {
		b.mu.Unlock()
		return nil
	}
	records := make(map[string]direction, len(cfg.Leaders))
	for _, id := range cfg.Leaders {
		if id == "" || id == cfg.Main {
			continue
		}
		if d := b.m[id]; loaded && d != nil {
			records[id] = *d
		} else {
			records[id] = direction{} // the title is the only available fallback
		}
	}
	if includeEnded && loaded {
		for id, d := range b.m {
			if id != "" && id != cfg.Main && d != nil {
				records[id] = *d
			}
		}
	}
	b.mu.Unlock()

	// A list validator needs only projects that supply a direction's fallback name.
	opened := s.kb.cur.Load()
	available := opened != nil && s.kbOn(ctx)
	projects := map[string]string{}
	names := make(map[string]string, len(records))
	for id, d := range records {
		if _, known := projects[d.Project]; available && d.Name == "" && d.Project != "" && !known {
			projects[d.Project] = ""
			if project, ok := opened.eng.Store.Project(d.Project); ok {
				projects[d.Project] = project.Name
			}
		}
		names[id] = nameOf(&d, titles[id].title, projects, available)
	}
	return names
}

// leaderNames are what the directions of leaders ids are called now (directionName), read once for all of them; a
// leader with no name at all is called by its id.
func (s *Service) leaderNames(ctx context.Context, ids []string) map[string]string {
	titles, _ := s.conversationTitles()
	pn, ok := s.projectNames(ctx)
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		var name string
		if d := s.directionOf(id); d != nil {
			name = nameOf(d, titles[id].title, pn, ok)
		} else {
			name = directionName(nil, titles[id].title, "")
		}
		out[id] = cmp.Or(clip(oneLine(name), 40), id)
	}
	return out
}

// briefLeaders are the current leaders as the briefings list them, one line each (#3609): the direction's name, its
// open tasks, how many wait for the owner (a review that needs the owner's decision, a plan waiting for Start counts
// with the plans) and how many were reported stuck in the last day, and its leader's latest summary of the day.
func (s *Service) briefLeaders(ctx context.Context, now time.Time) []prompts.BriefLeader {
	cfg := s.settings(ctx)
	var out []prompts.BriefLeader
	stuck := map[string]time.Time{}
	b := &s.lreports
	b.mu.Lock()
	if s.leaderReportsLocked(ctx) {
		maps.Copy(stuck, b.told.Stuck)
	}
	b.mu.Unlock()
	var todos []Todo // every supervised task, as the leaders' reports read them (leader_reports.go)
	s.todos.mu.RLock()
	for _, t := range s.todos.todos {
		if t.Supervision != nil && t.DoneAt == nil {
			todos = append(todos, copyTodoLocked(t))
		}
	}
	s.todos.mu.RUnlock()
	names := s.leaderNames(ctx, cfg.Leaders)
	for _, id := range cfg.Leaders {
		if id == "" || id == cfg.Main {
			continue
		}
		bl := prompts.BriefLeader{Name: names[id]}
		for _, t := range todos {
			if t.DoneAt != nil || t.Supervision == nil || t.Supervision.Supervisor != id {
				continue
			}
			bl.Open++
			switch at, ok := stuck[t.ID]; {
			case t.Supervision.state(t) == reviewNeedsUser:
				bl.Waiting++ // counted once: waiting on the owner, whether or not it was also reported stuck
			case ok && now.Sub(at) < 24*time.Hour:
				bl.Stuck++
			}
		}
		if d := s.directionOf(id); d != nil && d.Progress != nil && now.Sub(d.Progress.At) < 24*time.Hour {
			p := d.Progress
			bl.Reached, bl.Doing, bl.NeedsYou = clip(p.Reached, 120), clip(p.Doing, 120), clip(p.NeedsYou, 120)
		}
		out = append(out, bl)
	}
	return out
}
