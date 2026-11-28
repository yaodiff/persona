package db

import (
	"context"
	"io/fs"
	"testing"
	"time"
)

func TestInboxDraftSourceReplyMigration(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, migratedDatabase(t, ManagerSet), ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	at := time.Date(2026, 11, 2, 9, 0, 0, 708512000, time.UTC)
	if err := d.PutInboxItem(ctx, "example:1", []byte(`{"chain":[
		{"kind":"self_replied","at":"2026-11-02T09:00:00.708512489Z"},
		{"kind":"self_replied","at":"2026-11-02T09:01:00.708512489Z"},
		{"kind":"sent","at":"2026-11-02T09:01:00.708512489Z"}
	]}`), at, nil); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id, body string
	}{
		{"first", `{"draft":"first draft","sent_at":"2026-11-02T09:00:00.708512489Z"}`},
		{"second", `{"draft":"second draft","sent_at":"2026-11-02T09:00:00.708512489Z"}`},
		{"with_text", `{"sent":"actual reply","sent_at":"2026-11-02T09:00:00.708512489Z"}`},
		{"with_send", `{"sent_at":"2026-11-02T09:01:00.708512489Z"}`},
		{"other_time", `{"sent_at":"2026-11-02T09:02:00.708512489Z"}`},
	} {
		if err := d.AppendFeed(ctx, FeedRow{ID: row.id, Conversation: "conv", Kind: "message", Ref: "example:1",
			At: at.Add(-time.Minute), Body: []byte(row.body)}); err != nil {
			t.Fatal(err)
		}
	}
	for id, data := range map[string]string{
		"missing_chain": `{}`, "null_chain": `{"chain":null}`, "object_chain": `{"chain":{}}`,
	} {
		if err := d.PutInboxItem(ctx, id, []byte(data), at, nil); err != nil {
			t.Fatal(err)
		}
		if err := d.AppendFeed(ctx, FeedRow{ID: id, Conversation: "conv", Kind: "message", Ref: id, At: at,
			Body: []byte(`{"sent_at":"2026-11-02T09:00:00.708512489Z"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	files, err := fs.Glob(migrations, "migrations/manager/*_inbox_draft_source_replies.sql")
	if err != nil || len(files) != 1 {
		t.Fatalf("migration: %v, %v", files, err)
	}
	sql, err := fs.ReadFile(migrations, files[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []int64{2, 0} {
		tag, err := d.pool.Exec(ctx, string(sql))
		if err != nil || tag.RowsAffected() != want {
			t.Fatalf("affected %d rows, want %d: %v", tag.RowsAffected(), want, err)
		}
	}
	rows, err := d.Feed(ctx, "conv", at.Add(3*time.Minute), 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("incremental feed: %v, %v", rows, err)
	}
	for _, row := range rows {
		if want := `{"draft": "` + row.ID + ` draft"}`; string(row.Body) != want {
			t.Errorf("%s: got %s, want %s", row.ID, row.Body, want)
		}
	}
}
