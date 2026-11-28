package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/kb"
)

type kbSentFailure struct{ *kbStore }

func (*kbSentFailure) SentAsUsersForArchive(context.Context, string, time.Time, time.Time) ([]db.SentAsUser, error) {
	return nil, errors.New("ledger unavailable")
}

func TestKBDaySentDatabaseAndTimezone(t *testing.T) {
	needsKnowledgeUIModule(t)
	database, err := db.Open(context.Background(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	s := kbServiceOn(t, newFakeHub(), database, t.TempDir(), nil)
	e := kbEng(t, s)
	zone, err := time.LoadLocation("Etc/GMT-8")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.Rezone(zone); err != nil {
		t.Fatal(err)
	}
	chat := &kb.Chat{ID: "example-sent-thread", Source: "example", SourceID: "thread", Name: "Example"}
	if err := e.Store.PutChat(chat); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 3, 0, 0, 0, 0, zone)
	// i18n-ignore: the archive's canonical owner label
	owner := "我"
	if err := e.Store.AppendMessages(chat, []kb.Line{
		{At: at, Sender: owner, Text: "unique\n  content"},
		{At: at.Add(10 * time.Minute), Sender: owner, Text: "unique content"},
		{At: at.Add(-time.Minute), Sender: owner, Text: "boundary duplicate"},
		{At: at, Sender: owner, Text: "boundary duplicate"},
		{At: at, Sender: "Example", Text: "someone else"},
	}); err != nil {
		t.Fatal(err)
	}
	id, err := database.RecordSentAsUser(context.Background(), db.SentAsUser{At: at.Add(-15 * time.Second), Connector: "example", Target: json.RawMessage(`{"chat":"thread"}`), Text: "unique content"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.MarkSentAsUserRecalled(context.Background(), id, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"boundary duplicate", "someone else"} {
		if _, err := database.RecordSentAsUser(context.Background(), db.SentAsUser{At: at.Add(-15 * time.Second), Connector: "example", Target: json.RawMessage(`{"chat":"thread"}`), Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	for _, day := range []string{"2026-01-02", "2026-01-03"} {
		params, _ := json.Marshal(assistantwire.KBDayParams{ID: chat.ID, Day: day})
		out, ok, err := s.kbRequest(context.Background(), assistantwire.MethodKBExtensionDay, params)
		if err != nil || !ok {
			t.Fatalf("RPC: %v %v", ok, err)
		}
		got := out.(assistantwire.KBDayResult)
		if got.SentOrigin != "available" {
			t.Fatal(got)
		}
		for i, m := range got.Messages {
			want := day == "2026-01-03" && i == 0
			if (m.SentBy == "assistant") != want || (want && (m.SentLedgerID != id || m.RecalledAt == nil)) {
				t.Fatalf("%s row %d: %+v", day, i, m)
			}
		}
	}
	// Direct, ordinary kb.day never leaks privileged markers.
	out, _, err := s.kbRequest(context.Background(), assistantwire.MethodKBDay, json.RawMessage(`{"id":"example-sent-thread","day":"2026-01-03"}`))
	if err != nil {
		t.Fatal(err)
	}
	plain := out.(assistantwire.KBDayResult)
	if plain.SentOrigin != "unavailable" || plain.Messages[0].SentBy != "" {
		t.Fatal(plain)
	}
}

func TestKBDaySentStoreFailure(t *testing.T) {
	needsKnowledgeUIModule(t)
	for _, store := range []Store{newKBStore(), &kbSentFailure{newKBStore()}} {
		s := kbServiceOn(t, newFakeHub(), store, t.TempDir(), nil)
		e := kbEng(t, s)
		chat := &kb.Chat{ID: "example-thread", Source: "example", SourceID: "thread"}
		if err := e.Store.PutChat(chat); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-time.Hour)
		if err := e.Store.AppendMessages(chat, []kb.Line{{At: at, Sender: "Example", Text: "content"}}); err != nil {
			t.Fatal(err)
		}
		got, err := s.kbDaySent(context.Background(), e, assistantwire.KBDayParams{ID: chat.ID, Day: at.In(e.Store.Zone()).Format("2006-01-02")})
		_, failure := store.(*kbSentFailure)
		if failure && err == nil || !failure && (err != nil || got.SentOrigin != "unavailable") {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	}
}

type kbSentSource struct{}

func (kbSentSource) Name() string                                        { return "example" }
func (kbSentSource) Chats(context.Context, int) ([]kb.SourceChat, error) { return nil, nil }
func (kbSentSource) Messages(context.Context, kb.SourceChat, string, int) ([]kb.SourceMessage, string, bool, error) {
	return nil, "", false, nil
}
func (kbSentSource) Download(context.Context, string, string) error { return nil }

func TestKBDaySentIngestedReference(t *testing.T) {
	needsKnowledgeUIModule(t)
	database, err := db.Open(context.Background(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	s := kbServiceOn(t, newFakeHub(), database, t.TempDir(), nil)
	e := kbEng(t, s)
	recent := time.Now().In(e.Store.Zone()).AddDate(0, 0, -1)
	at := time.Date(recent.Year(), recent.Month(), recent.Day(), 12, 0, 0, 0, e.Store.Zone())
	n, err := e.Ingest(context.Background(), kbSentSource{}, kb.SourceChat{ID: "thread", Name: "Example", Kind: "group"}, []kb.SourceMessage{
		{ID: "assistant-ref", At: at, Text: "archived rendering", Mine: true},
		{ID: "human-ref", At: at.Add(time.Minute), Text: "ledger words", Mine: true},
	})
	if err != nil || n != 2 {
		t.Fatalf("ingest %d: %v", n, err)
	}
	id, err := database.RecordSentAsUser(context.Background(), db.SentAsUser{At: at, Connector: "example", Target: json.RawMessage(`{"chat":"thread"}`), Text: "ledger words", MessageRef: "assistant-ref"})
	if err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(assistantwire.KBDayParams{ID: "example-thread", Day: at.In(e.Store.Zone()).Format("2006-01-02")})
	out, ok, err := s.kbRequest(context.Background(), assistantwire.MethodKBExtensionDay, params)
	if err != nil || !ok {
		t.Fatalf("RPC handled=%v error=%v", ok, err)
	}
	got := out.(assistantwire.KBDayResult)
	if len(got.Messages) != 2 || got.Messages[0].SentLedgerID != id || got.Messages[1].SentBy != "" {
		t.Fatalf("ref matching: %+v", got)
	}
}
