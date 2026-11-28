package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

const (
	supervisionWake  = "wake"
	supervisionDefer = "defer"
)

const supervisionTriageTimeout = 8 * time.Second
const supervisionTriageSettingKey = "assistant.supervision_triage"

func (v Settings) supervisionTriageOn() bool {
	return v.SupervisionTriage != nil && *v.SupervisionTriage
}

func (s *Service) supervisionTriageOn(ctx context.Context) bool {
	return s.settings(ctx).supervisionTriageOn()
}

type supervisionDecisionStore interface {
	SupervisionTriage(context.Context, []string) (map[string]string, error)
	SaveSupervisionTriage(context.Context, []db.SupervisionTriageRow) (map[string]string, error)
	DeferredSupervision(context.Context, time.Time, time.Time) ([]db.SupervisionTriageRow, error)
	SupervisionBriefed(context.Context, []string, time.Time) error
}

// Eligibility is an allowlist. Owner decisions, explicit outcomes and active PR
// conflicts take their ordinary delivery path, even when mixed with routine news.
func supervisionTriageEligible(g superviseSignal) bool {
	switch g.Kind {
	case sigIdle:
		return !g.Handoff
	case sigStalled, sigNoProgress, sigEstimateDue, sigOverdue, sigOrphaned, sigUnjudged, sigDraftPR:
		return true
	case sigPREvent:
		return g.Report != prEventKindConflict
	default:
		return false
	}
}

type supervisionTriageItem struct {
	Number  int    `json:"number"`
	Signal  string `json:"signal"`
	Title   string `json:"title"`
	Detail  string `json:"detail,omitempty"`
	Minutes int    `json:"minutes,omitempty"`
	Orphan  string `json:"orphan,omitempty"`
}

type supervisionTriageVerdict struct {
	Number   int    `json:"number"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

func triageSupervisionWith(ctx context.Context, m jsonModel, lang i18n.Lang, items []supervisionTriageItem) []supervisionTriageVerdict {
	var reply struct {
		Verdicts []supervisionTriageVerdict `json:"verdicts"`
	}
	input, _ := json.Marshal(items)
	err := m.JSON(ctx, prompts.RenderSupervisionTriage(answerLanguage(string(lang))), string(input), &reply)
	fallback := "assistant-briefing.supervision_triage_invalid"
	if err != nil {
		fallback = "assistant-briefing.supervision_triage_unavailable"
	}
	result := make([]supervisionTriageVerdict, len(items))
	seen := make([]bool, len(items))
	if err == nil {
		for _, v := range reply.Verdicts {
			i := v.Number - 1
			if i < 0 || i >= len(items) {
				continue
			}
			if seen[i] {
				result[i] = supervisionTriageVerdict{}
				continue
			}
			seen[i] = true
			v.Reason = clip(oneLine(v.Reason), 180)
			if (v.Decision == supervisionWake || v.Decision == supervisionDefer) && v.Reason != "" {
				result[i] = v
			}
		}
	}
	for i := range result {
		if result[i].Decision == "" {
			result[i] = supervisionTriageVerdict{i + 1, supervisionDefer, i18n.T(lang, fallback)}
		}
	}
	return result
}

func supervisionTriageID(g superviseSignal) string {
	raw, _ := json.Marshal([2]string{g.id(), g.Key})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// screenModelSupervision keeps the two delivery slices aligned. Eligible signals
// whose verdict cannot be saved stay in the outbox. A deferred verdict takes the
// place of a wake only after the briefing can read it.
func (s *Service) screenModelSupervision(ctx context.Context, recipient string, entries []prompts.SuperviseEntry, signals []superviseSignal, now, prev time.Time) ([]prompts.SuperviseEntry, []superviseSignal) {
	if !s.supervisionTriageOn(ctx) {
		return entries, signals
	}
	var ids []string
	index := make(map[int]string)
	for i, g := range signals {
		if !supervisionTriageEligible(g) {
			continue
		}
		if t, ok := s.todoNow(g.Todo); ok && ownerWaitSignal(t, g) {
			continue
		}
		id := supervisionTriageID(g)
		index[i] = id
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return entries, signals
	}
	verdicts, err := s.supervisionVerdicts(ctx, recipient, entries, signals, ids, index)
	if err != nil {
		s.logf("supervise: triage verdicts unavailable in storage: %v", err)
	}
	var wakeEntries []prompts.SuperviseEntry
	var wake, deferred, obsolete, retry []superviseSignal
	for i, g := range signals {
		id, eligible := index[i]
		switch {
		case eligible && s.routineSignalSuppressed(g, time.Now()):
			obsolete = append(obsolete, g) // the task changed while the verdict was pending
		case !eligible, verdicts[id] == supervisionWake:
			wakeEntries, wake = append(wakeEntries, entries[i]), append(wake, g)
		case verdicts[id] == supervisionDefer:
			deferred = append(deferred, g)
		default:
			retry = append(retry, g)
		}
	}
	s.estimateDueTold(ctx, deferred)
	settled := append(deferred, obsolete...)
	if len(settled) > 0 {
		s.turnsTold(ctx, settled)
		s.ackSupervisePending(settled)
	}
	if len(retry) > 0 {
		if len(wake) > 0 {
			now = time.Time{}
		}
		s.restoreDigest(retry, now, prev)
	} else if len(wake) == 0 {
		b := &s.supervise
		b.mu.Lock()
		if b.last.Equal(now) {
			b.last = prev
		}
		b.mu.Unlock()
	}
	return wakeEntries, wake
}

func (s *Service) supervisionVerdicts(ctx context.Context, recipient string, entries []prompts.SuperviseEntry, signals []superviseSignal, ids []string, index map[int]string) (map[string]string, error) {
	st, ok := s.store().(supervisionDecisionStore)
	if !ok {
		return nil, errors.New("supervision verdict store unavailable")
	}
	verdicts, err := st.SupervisionTriage(ctx, ids)
	if err != nil {
		return nil, err
	}
	var items []supervisionTriageItem
	var rows []db.SupervisionTriageRow
	for i, e := range entries {
		id, eligible := index[i]
		if !eligible || verdicts[id] != "" {
			continue
		}
		g := signals[i]
		kind := g.Kind
		if g.Kind == sigPREvent {
			kind = g.Report
		}
		title, detail := clip(oneLine(e.Title), 120), clip(oneLine(e.Detail), 240)
		items = append(items, supervisionTriageItem{len(items) + 1, kind, title, detail, e.Minutes, e.Orphan})
		rows = append(rows, db.SupervisionTriageRow{
			ID: id, TodoID: g.Todo, Recipient: recipient, Signal: kind, SignalKey: g.Key, Title: title, Detail: detail,
		})
	}
	if len(items) == 0 {
		return verdicts, nil
	}
	lang := i18n.Resolve(s.hello(ctx).Locale)
	modelCtx, cancel := context.WithTimeout(ctx, supervisionTriageTimeout)
	decisions := triageSupervisionWith(modelCtx, s.purposeModelFor("inbox.triage", "supervision_triage"), lang, items)
	cancel()
	at := time.Now().UTC()
	for i, v := range decisions {
		rows[i].Decision, rows[i].Reason, rows[i].At = v.Decision, v.Reason, at
	}
	saved, err := st.SaveSupervisionTriage(ctx, rows)
	if err == nil {
		maps.Copy(verdicts, saved)
	}
	return verdicts, err
}
