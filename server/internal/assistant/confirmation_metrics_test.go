package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestConfirmationMetricsDatabase(t *testing.T) {
	ctx := context.Background()
	dsn := newMigratedDatabase(t, db.ManagerSet)
	database, err := db.Open(ctx, dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 1, 0, 0, 0, loc)
	midnight := time.Date(2026, 11, 11, 0, 0, 0, 0, loc)
	cards := []proto.Interaction{
		{ID: "notified", MachineID: "computer", SessionID: "builder", SessionTitle: "Build the app", Kind: proto.IxQuestion, CreatedAt: now.Add(-48 * time.Hour)},
		{ID: "automatic", MachineID: "computer", SessionID: "builder", SessionTitle: "Build the app", Kind: proto.IxPlan, CreatedAt: now},
		{ID: "supervisor", MachineID: "computer", SessionID: "builder", Kind: proto.IxQuestion, CreatedAt: now},
		{ID: "quiet", MachineID: "computer", SessionID: "builder", Kind: proto.IxPermission, CreatedAt: now},
		{ID: "yesterday", MachineID: "computer", SessionID: "builder", Kind: proto.IxPermission, CreatedAt: midnight.Add(-time.Hour)},
		{ID: "future", MachineID: "computer", SessionID: "builder", Kind: "wait", CreatedAt: now},
	}
	for _, ix := range cards {
		if err := database.InsertInteraction(ctx, ix); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.MarkConfirmationNotified(ctx, cards[0].ID, cards[0].CreatedAt, midnight, "task-build"); err != nil {
		t.Fatal(err)
	}
	// A retry tomorrow must retain the first dispatch, rather than moving today's count.
	if err := database.MarkConfirmationNotified(ctx, cards[0].ID, cards[0].CreatedAt, now.Add(24*time.Hour), "different-task"); err != nil {
		t.Fatal(err)
	}
	if err := database.MarkConfirmationAutoAuthorized(ctx, cards[1].ID, cards[1].CreatedAt, now, "task-build", "grant-merge"); err != nil {
		t.Fatal(err)
	}
	if err := database.MarkConfirmationAutoAuthorized(ctx, cards[1].ID, cards[1].CreatedAt, now.Add(24*time.Hour), "different-task", "different-grant"); err != nil {
		t.Fatal(err)
	}
	if err := database.MarkConfirmationNotified(ctx, cards[4].ID, cards[4].CreatedAt, midnight.Add(-time.Microsecond), ""); err != nil {
		t.Fatal(err)
	}
	if err := database.MarkConfirmationNotified(ctx, cards[5].ID, cards[5].CreatedAt, now.Add(time.Microsecond), ""); err != nil {
		t.Fatal(err)
	}
	restarted, err := db.Open(ctx, dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Close)
	s, _ := newQuietServiceOn(t, restarted)
	c := s.briefConfirmationsAt(ctx, now, loc)
	if c.Unknown || !c.Since.Equal(midnight) || c.UserNotified != 1 || c.AutoAuthorized != 1 || len(c.Sources) != 2 {
		t.Fatalf("local day's counts after restart: %+v", c)
	}
	for _, row := range c.Sources {
		if row.TodoID != "task-build" || row.MachineID != "computer" || row.SessionID != "builder" || row.SessionTitle != "Build the app" {
			t.Fatalf("source attribution changed: %+v", row)
		}
	}
	if got := countIn(t, dsn, `SELECT count(*) FROM interactions WHERE id = 'automatic' AND authorization_id = 'grant-merge'`); got != 1 {
		t.Fatal("first grant attribution was overwritten")
	}
	setSettings(t, s, func(v *Settings) { v.Timezone = "Asia/Tokyo" })
	if !s.briefingOn() {
		return
	}
	f := s.briefFactsAt(ctx, now, "")
	if f.Confirmations.Unknown || f.Confirmations.UserNotified != 1 || f.Confirmations.AutoAuthorized != 1 || !f.Confirmations.Since.Equal(midnight) {
		t.Fatalf("routine did not take the daily counts: %+v", f.Confirmations)
	}
	f.Lang = "en"
	f.ConfirmationLines = s.confirmationLines(i18n.En, s.briefConfirmationsAt(ctx, now, s.location(ctx)))
	card, plain, _ := s.briefCard(f, "Morning briefing", "")
	for _, want := range []string{"Today: 1 confirmation cards sent to you", "1 automatically authorized", "Build the app", "question", "plan"} {
		if !strings.Contains(card.JSON(), want) || !strings.Contains(plain, want) || !strings.Contains(s.briefModelText(f), want) {
			t.Fatalf("briefing omitted %q: card=%s\nplain=%s\nmodel=%s", want, card.JSON(), plain, s.briefModelText(f))
		}
	}
	body, err := json.Marshal(s.briefBodyOf(f, "Morning briefing"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"user_notified":1`) || !strings.Contains(string(body), `"auto_authorized":1`) {
		t.Fatalf("feed counts absent: %s", body)
	}
	t.Logf("BRIEFING_RENDER\n%s", plain)
	t.Logf("BRIEFING_FEED %s", body)
}

type unavailableConfirmationStore struct{ Store }

func (unavailableConfirmationStore) MarkConfirmationNotified(context.Context, string, time.Time, time.Time, string) error {
	return errors.New("unavailable")
}
func (unavailableConfirmationStore) MarkConfirmationAutoAuthorized(context.Context, string, time.Time, time.Time, string, string) error {
	return errors.New("unavailable")
}
func (unavailableConfirmationStore) ConfirmationCounts(context.Context, time.Time, time.Time) ([]db.ConfirmationCount, error) {
	return nil, errors.New("unavailable")
}

func TestConfirmationMetricsUnavailable(t *testing.T) {
	s := newFleetTest(t).s
	noDirectionsTimers(s)
	base := s.store()
	s.setStore(func() Store { return unavailableConfirmationStore{Store: base} })
	c := s.briefConfirmationsAt(context.Background(), time.Now(), time.UTC)
	if !c.Unknown || !strings.Contains(c.summary(i18n.Lang("en")), "unknown") {
		t.Fatalf("read failure became a zero count: %+v", c)
	}
}

func TestGoldenConfirmationBriefFacts(t *testing.T) {
	golden(t, "routine_facts.confirmations", prompts.RenderBriefFacts(prompts.BriefFacts{Hours: 12,
		ConfirmationSummary: "Today: 3 confirmation cards sent to you · 2 automatically authorized",
		Confirmations:       []string{"Build the app · question: 2 sent to you · 1 automatically authorized", "Review the app · plan: 1 sent to you · 1 automatically authorized"}}))
}

func TestConfirmationMetricsDeliveryBoundary(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	s, _ := newQuietServiceOn(t, database)
	ix := proto.Interaction{ID: "delivery-boundary", MachineID: "computer", SessionID: "builder", Kind: proto.IxPermission, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	s.record(ix)
	s.show(ix)
	s.tellApps(ix)
	before := s.briefConfirmationsAt(ctx, time.Now(), time.UTC)
	if before.UserNotified != 0 || before.AutoAuthorized != 0 {
		t.Fatalf("app display counted as a user notification: %+v", before)
	}
	s.tellUser(ix, false)
	s.tellUser(ix, false)
	after := s.briefConfirmationsAt(ctx, time.Now(), time.UTC)
	if after.Unknown || after.UserNotified != 1 || len(after.Sources) != 1 || after.Sources[0].Kind != proto.IxPermission {
		t.Fatalf("user-layer dispatch not counted exactly once: %+v", after)
	}
}
