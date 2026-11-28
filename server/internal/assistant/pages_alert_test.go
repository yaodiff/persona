package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Every `pushes` sequence in spec/fixtures/dashboards/watch.json runs through the real reading, switch, rule-change
// and pageReadingWritten path against real Postgres, capturing pushes and verifying exact push/no-push behavior,
// card ledger category 1 (page_alert), P1 level, title, body and related key.
func TestWatchFixturePushesSequences(t *testing.T) {
	fx := loadWatchFixture(t)
	if len(fx.Pushes) != 7 {
		t.Fatalf("fixture has %d push sequences, want 7", len(fx.Pushes))
	}
	s, hub, database := pagesTestService(t)
	hub.fakeHub.hello.Locale = string(i18n.En)
	ctx := context.Background()
	beforeLedger := cardsToday(t, s)[cardPageAlert].N

	const baseWatch = `{"question":"Rent for a 1-bed on Elm Street","display":"number","unit":"USD","alert":{"when":"below","value":2000}}`

	for idx, seq := range fx.Pushes {
		t.Run(seq.Name, func(t *testing.T) {
			// Stagger draft creation >24h apart so all 7 fixture sequences fit under the 5-drafts-per-24h cap.
			start := time.Date(2026, 11, 25, 7, 0, 0, 0, time.UTC).AddDate(0, 0, -2*(len(fx.Pushes)-idx))
			p := keptWatchPage(t, database, "Rent "+seq.Name, "money", baseWatch, "", true, seq.AlertOn, start)

			for i, step := range seq.Steps {
				at, err := time.Parse(time.RFC3339, step.At)
				if err != nil {
					t.Fatalf("step %d at: %v", i, err)
				}
				before := len(hub.fakeHub.pushed())

				switch {
				case step.Switch != "":
					onVal := "false"
					if step.Switch == "on" {
						onVal = "true"
					}
					mustPage(t, s, p.ID, wire.UIRequest{Action: "persona.alert", Values: map[string]string{"on": onVal}})
				case len(step.Rule) > 0:
					cur, err := database.AssistantPage(ctx, p.ID)
					if err != nil {
						t.Fatal(err)
					}
					var wObj map[string]any
					if err := json.Unmarshal(cur.Watch, &wObj); err != nil {
						t.Fatal(err)
					}
					var ruleObj any
					if err := json.Unmarshal(step.Rule, &ruleObj); err != nil {
						t.Fatal(err)
					}
					wObj["alert"] = ruleObj
					newWatch, err := json.Marshal(wObj)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := database.UpdateAssistantPage(ctx, db.AssistantPageUpdate{
						ID:       p.ID,
						Expected: cur.Version,
						View:     cur.View,
						Watch:    newWatch,
						By:       db.AssistantPageWriter{Kind: db.AssistantPageByAgent, Machine: "server", Session: "main-1"},
						At:       at,
					}); err != nil {
						t.Fatalf("step %d UpdateAssistantPage: %v", i, err)
					}
				case step.Value != nil:
					raw, _ := json.Marshal(map[string]float64{"value": *step.Value})
					updated, _, err := database.AddAssistantPageReading(ctx, db.NewAssistantPageReading{
						PageID:  p.ID,
						Reading: raw,
						ByKind:  db.AssistantPageByRoutine,
						At:      at,
					})
					if err != nil {
						t.Fatalf("step %d AddAssistantPageReading: %v", i, err)
					}
					s.pageReadingWritten(ctx, updated)
				default:
					t.Fatalf("step %d has no action", i)
				}

				pushed := hub.fakeHub.pushed()[before:]
				gotPush := len(pushed) > 0
				if gotPush != step.Push {
					t.Fatalf("step %d (%s): pushed=%v (%+v), want %v", i, step.At, gotPush, pushed, step.Push)
				}
				if step.Push {
					if len(pushed) != 1 {
						t.Fatalf("step %d: got %d pushes, want 1", i, len(pushed))
					}
					n := pushed[0]
					if n.Title != p.Title || n.Level != assistantwire.LevelP1 || n.RelatedKey != "page-alert:"+p.ID {
						t.Fatalf("step %d push metadata: %+v", i, n)
					}
					wantReading := formatWatchValue(*step.Value, "USD")
					if !strings.Contains(n.Body, "Rent for a 1-bed on Elm Street") || !strings.Contains(n.Body, wantReading) {
						t.Fatalf("step %d push body = %q, want question and %q", i, n.Body, wantReading)
					}
				}
			}
		})
	}

	// Verify card ledger recorded every page_alert push under category 7 (cardCatSubscribed).
	ledger := cardsToday(t, s)
	c, ok := ledger[cardPageAlert]
	if !ok || c.Category != cardCatSubscribed || c.N-beforeLedger != 6 {
		t.Fatalf("card ledger for page_alert = %+v (before %d), want category %d delta 6", c, beforeLedger, cardCatSubscribed)
	}
}

// alert_text is included in the push body when present, deep link points to /extension/-pages/<id>, and
// pageReadingWritten invalidates the page, Dashboards index and Overview card.
func TestWatchAlertTextDeepLinkAndInvalidations(t *testing.T) {
	s, hub, database := pagesTestService(t)
	hub.fakeHub.hello.Locale = string(i18n.En)
	ctx := context.Background()
	now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)

	watchJSON := `{"question":"Rent for a 1-bed on Elm Street","display":"number","unit":"USD","alert":{"when":"below","value":2000}}`
	p := keptWatchPage(t, database, "Elm Street rent", "money", watchJSON, "", true, true, now.Add(-time.Hour))

	if got := pageAlertDeepLink(p.ID); got != "/extension/-pages/"+p.ID {
		t.Fatalf("deep link = %q, want /extension/-pages/%s", got, p.ID)
	}

	updated, _, err := database.AddAssistantPageReading(ctx, db.NewAssistantPageReading{
		PageID:    p.ID,
		Reading:   json.RawMessage(`{"value":1950}`),
		AlertText: "First time under 2,000 this month",
		ByKind:    db.AssistantPageByRoutine,
		At:        now,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.pageReadingWritten(ctx, updated)

	pushed := hub.fakeHub.pushed()
	if len(pushed) != 1 {
		t.Fatalf("got %d pushes, want 1", len(pushed))
	}
	wantBody := "Rent for a 1-bed on Elm Street: 1,950 USD — First time under 2,000 this month"
	if pushed[0].Body != wantBody || pushed[0].Title != "Elm Street rent" || pushed[0].Level != assistantwire.LevelP1 || pushed[0].RelatedKey != "page-alert:"+p.ID {
		t.Fatalf("unexpected push: %+v", pushed[0])
	}

	// Verify invalidations queued for page, index, and Overview card g_<hex>.
	s.pages.events.mu.Lock()
	_, pagePending := s.pages.events.queue.pending[p.ID]
	_, idxPending := s.pages.events.queue.pending[pagesIndex]
	_, ovPending := s.pages.events.queue.pending[overviewCardID(p.ID)]
	s.pages.events.mu.Unlock()
	if !pagePending || !idxPending || !ovPending {
		t.Fatalf("missing invalidations: page=%v index=%v overview=%v", pagePending, idxPending, ovPending)
	}
}

// Nothing else pushes: draft create, page update, non-tripping reading, manual refresh, and failed check never push
// and never enter the card ledger.
func TestWatchNothingElsePushes(t *testing.T) {
	s, hub, database := pagesTestService(t)
	s.turnPoll = 10 * time.Millisecond
	ctx := context.Background()
	beforeLedger := cardsToday(t, s)[cardPageAlert].N
	now := time.Now().UTC()

	watchJSON := `{"question":"Rent for a 1-bed on Elm Street","display":"number","unit":"USD","alert":{"when":"below","value":2000}}`

	// 1. Draft create.
	draftID := db.NewAssistantPageID()
	if _, err := database.CreateAssistantPage(ctx, db.NewAssistantPage{
		ID: draftID, Title: "Draft rent", Icon: "money", OwnerKind: db.AssistantPageOwnerMain,
		CreatedMachine: "server", CreatedSession: "main-1", OfferView: "view-" + draftID,
		Watch: json.RawMessage(watchJSON), At: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	// 2. Keep with alert ON + update page.
	p, _, err := database.KeepAssistantPage(ctx, db.AssistantPageKeep{OfferView: "view-" + draftID, Device: "phone", AlertOn: true, At: now.Add(-30 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateAssistantPage(ctx, db.AssistantPageUpdate{
		ID: p.ID, Expected: p.Version, View: p.View, Watch: p.Watch,
		By: db.AssistantPageWriter{Kind: db.AssistantPageByAgent, Machine: "server", Session: "main-1"}, At: now.Add(-20 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	// 3. Reading without trip.
	updated, _, err := database.AddAssistantPageReading(ctx, db.NewAssistantPageReading{
		PageID: p.ID, Reading: json.RawMessage(`{"value":2150}`), ByKind: db.AssistantPageByRoutine, At: now.Add(-10 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.pageReadingWritten(ctx, updated)

	// 4. Refresh + failed check.
	saveTestRoutine(t, s, Task{
		ID: "rt_nopush", Title: "Check rent", Prompt: "Check", Repeat: repeatDaily, Time: "20:00",
		Enabled: true, Push: "asked", CreatedBy: "assistant", CreatedAt: now.Add(-time.Hour),
		RunMachineID: "server", RunSessionID: "rt-nopush-1", RunLauncher: "claude",
	})
	hub.fakeHub.setSessions("server", proto.Session{SessionID: "main-1", Assistant: true}, proto.Session{SessionID: "rt-nopush-1", Assistant: true})
	hub.fakeHub.mu.Lock()
	hub.fakeHub.failAssistant = errors.New("offline")
	hub.fakeHub.failSend = errors.New("offline")
	hub.fakeHub.failStart = errors.New("offline")
	hub.fakeHub.mu.Unlock()
	if _, _, err := database.BindAssistantPageRoutine(ctx, p.ID, "rt_nopush", now); err != nil {
		t.Fatal(err)
	}
	mustPage(t, s, p.ID, wire.UIRequest{Action: "persona.refresh"})
	waitFor(t, "failed refresh settled", func() bool {
		return pageItem(mustPage(t, s, p.ID, wire.UIRequest{}).View, "persona.status").Text == "The check failed"
	})

	if pushed := hub.fakeHub.pushed(); len(pushed) != 0 {
		t.Fatalf("expected zero pushes from draft/update/non-tripping reading/refresh/failure, got: %+v", pushed)
	}
	if c := cardsToday(t, s)[cardPageAlert]; c.N != beforeLedger {
		t.Fatalf("expected zero new page_alert ledger entries, got delta %d (%+v)", c.N-beforeLedger, c)
	}
}
