package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/modapi"
)

// Scheduled routines use the loaded schedule facts runner, when present.

type (
	briefFacts = modapi.BriefSnapshot
	briefRun   = modapi.BriefRun
)

func briefTaskInfo(t Task) modapi.BriefTaskInfo {
	return modapi.BriefTaskInfo{
		ID:            t.ID,
		Label:         t.label(),
		Contributions: t.routineContributions,
		Facts:         t.Facts,
		Report:        t.Report,
		SessionID:     t.SessionID,
		RunSessionID:  t.RunSessionID,
		MachineID:     t.MachineID,
	}
}

func briefScheduleRun(r ScheduleRun) modapi.BriefScheduleRun {
	return modapi.BriefScheduleRun{
		ID:        r.ID,
		RefID:     r.RefID,
		SessionID: r.SessionID,
		MachineID: r.MachineID,
		Due:       r.Due,
		Retried:   r.Retried,
		Manual:    r.Manual,
	}
}

func (s *Service) briefStart(ctx context.Context, t Task, now time.Time) string {
	r, ok := s.briefingRunner()
	if !ok {
		return ""
	}
	return r.Start(ctx, briefTaskInfo(t), now)
}

func (s *Service) briefReportStart(ctx context.Context, t Task, now time.Time) string {
	r, ok := s.briefingRunner()
	if !ok {
		return ""
	}
	return r.StartReport(ctx, briefTaskInfo(t), now)
}

func (s *Service) briefSent(run ScheduleRun) {
	if r, ok := s.briefingRunner(); ok {
		r.Sent(briefScheduleRun(run))
	}
}

func (s *Service) briefOf(session string) *briefRun {
	r, ok := s.briefingRunner()
	if !ok {
		return nil
	}
	return r.SessionRun(session)
}

func (s *Service) briefClaim(session, asked, turn string, cut bool) (*briefRun, bool) {
	r, ok := s.briefingRunner()
	if !ok {
		return nil, false
	}
	return r.Claim(session, asked, turn, cut)
}

func (s *Service) briefFailure(session, asked string, cut bool) bool {
	r, ok := s.briefingRunner()
	if !ok {
		return false
	}
	return r.IsFailure(session, asked, cut)
}

func (s *Service) briefFallback(ctx context.Context, scheduled ScheduleRun, t Task, m Device) {
	if r, ok := s.briefingRunner(); ok {
		r.Fallback(ctx, briefScheduleRun(scheduled), briefTaskInfo(t), modapi.BriefDevice{ID: m.ID, Name: m.Name})
	}
}

func (s *Service) briefNotStarted(ctx context.Context, t Task, failed ScheduleRun, retry bool) {
	if r, ok := s.briefingRunner(); ok {
		r.NotStarted(ctx, briefTaskInfo(t), briefScheduleRun(failed), retry)
	}
}

func (s *Service) briefPush(session string, l i18n.Lang, report bool) string {
	r, ok := s.briefingRunner()
	if !ok {
		return ""
	}
	return r.Push(session, string(l), report)
}

func (s *Service) briefReached(session string) {
	if r, ok := s.briefingRunner(); ok {
		r.Reached(session)
	}
}

func (s *Service) briefingGrace(ctx context.Context, local time.Time, start int) int {
	r, ok := s.briefingRunner()
	if !ok {
		return 0
	}
	return r.Grace(ctx, local, start)
}

// briefMirror sends the cards of a routine with facts in place of its answer's cards (feishuMirror): the model's answer
// on top, the facts under it. Required owner waits continue onto more cards rather than crossing Feishu's physical
// budget. The push port's per-index key makes every continuation retry independently and idempotently.
func (s *Service) briefMirror(ctx context.Context, machine, session, label string, run *briefRun, answer, turn string, delivery *TaskDelivery) {
	openLabel, openURL := s.openLink(ctx, machine, session)
	cards, _, shown := s.scheduleCards(run.Facts, run.Label, answer, "", openLabel, openURL)
	if len(cards) == 0 {
		return
	}
	sum := sha256.Sum256([]byte("brief\x00" + machine + "\x00" + session + "\x00" + turn + "\x00" + answer))
	to := s.larkMirror(machine, session, label, false, answer, turn, delivery)
	s.postBriefMirror(ctx, to, i18n.Lang(run.Facts.Lang), cards, "b"+hex.EncodeToString(sum[:8]), func() { s.briefAsked(run.Facts, shown) })
}

func (s *Service) postBriefMirror(ctx context.Context, to assistantwire.LarkMirror, lang i18n.Lang, pages []modapi.BriefCardPage, key string, sent func()) {
	drawings, fallbacks := make([]*botcard.Card, len(pages)), make([]string, len(pages))
	for i, page := range pages {
		drawings[i], fallbacks[i] = (*botcard.Card)(page.Card), page.Fallback
	}
	s.larkModule().MirrorPost(ctx, to, lang, drawings, fallbacks, key, sent)
}

func (s *Service) scheduleCards(f *briefFacts, label, answer, header, openLabel, openURL string) ([]modapi.BriefCardPage, string, []string) {
	r, ok := s.briefingRunner()
	if !ok {
		return nil, "", nil
	}
	return r.RenderCards(f, label, answer, header, openLabel, openURL)
}

// briefAsked notes that the briefing card with the Accept buttons of the tasks shown went out: those tasks are not
// reminded that day (remindAcceptance), the card asked for them. Only the round the card was made for counts.
func (s *Service) briefAsked(f *briefFacts, shown []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // not the mirror's: this may run on its retry
	defer cancel()
	s.setupTold(fromBriefSetup(f.Setup), f.At) // the card carried the week's block
	asked := map[string]time.Time{}
	for _, r := range f.Accept {
		asked[r.ID] = r.Asked
	}
	b := &s.accept
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadAcceptLocked(ctx) {
		return
	}
	for _, id := range shown {
		if a := b.asks[id]; a != nil && a.AskedAt.Equal(asked[id]) {
			a.RemindedAt = time.Now().UTC()
			a.ForBriefing = false // one that waited for a briefing is put to the owner on this card (decision_wait.go)
		}
	}
	s.saveAcceptLocked(ctx)
}
