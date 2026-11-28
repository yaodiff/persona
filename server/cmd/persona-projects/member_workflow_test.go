package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nwverse/persona/server/internal/db/dbtest"
	"github.com/nwverse/persona/server/internal/teamdocs"
)

// Go through the public member commands and the real HTTP handler. Each run owns a
// separate database so the role and approval checks exercise the PostgreSQL transaction.
func TestMemberRevisionWorkflow(t *testing.T) {
	base := os.Getenv(dbtest.Env)
	if base == "" {
		t.Skip(dbtest.Env + " not set")
	}
	ctx := t.Context()
	dsn, drop, err := dbtest.Own(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(drop)
	if dsn == base {
		t.Skip("requires a role that can create a test database")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := &teamdocs.Store{Pool: pool}
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(teamdocs.Handler(store))
	t.Cleanup(server.Close)

	const project, document = "garden", "planting-notes"
	const body = "Seedlings for the north bed.\r\nBring a second tray."
	digest := sha256.Sum256([]byte(body))
	sum := hex.EncodeToString(digest[:])
	dir := t.TempDir()
	input := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(input, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	credentials := make(map[string]string)
	tokenFiles := make(map[string]string)
	for _, role := range []string{"writer", "reader", "approver"} {
		token, err := store.Grant(ctx, project, role, role)
		if err != nil {
			t.Fatal(err)
		}
		credentials[role] = token
		tokenFiles[role] = filepath.Join(dir, role)
		if err := os.WriteFile(tokenFiles[role], []byte(token+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("TEAM_PROJECT_DATABASE_URL", "")
	t.Setenv("TEAM_PROJECT_URL", server.URL)
	command := func(role string, wantSuccess bool, args ...string) string {
		t.Helper()
		t.Setenv("TEAM_PROJECT_TOKEN_FILE", tokenFiles[role])
		var out bytes.Buffer
		err := runMemberCommand(ctx, append(args, project, document), &out)
		if (err == nil) != wantSuccess {
			t.Fatalf("%s as %s: success=%v, want %v (%v)", args[0], role, err == nil, wantSuccess, err)
		}
		if err != nil && (out.Len() != 0 || strings.Contains(err.Error(), body)) {
			t.Fatal("failed operation exposed document content or wrote a result")
		}
		for _, token := range credentials {
			if strings.Contains(out.String(), token) || err != nil && strings.Contains(err.Error(), token) {
				t.Fatal("member credential in output")
			}
		}
		return out.String()
	}
	checkReceipt := func(raw, approver string) {
		t.Helper()
		var r teamdocs.Revision
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(raw), &r) != nil || json.Unmarshal([]byte(raw), &fields) != nil {
			t.Fatal("expected a JSON receipt")
		}
		if _, found := fields["content"]; found || strings.Contains(raw, "Seedlings") {
			t.Fatal("receipt exposed document content")
		}
		if r.Project != project || r.Document != document || r.Number != 1 || r.SHA256 != sum || r.Author != "writer" || r.ApprovedBy != approver {
			t.Fatalf("incorrect receipt metadata for %s", document)
		}
	}

	publish := []string{"publish", "-expected", "0", "-file", input}
	checkReceipt(command("writer", true, publish...), "")
	command("writer", false, publish...) // the head already advanced
	if got := command("reader", true, "read", "-revision", "1"); got != body {
		t.Fatal("read changed the bytes reviewed")
	}
	command("reader", false, "publish", "-expected", "1", "-file", input)
	command("reader", false, "approve", "-revision", "1", "-sha256", sum)
	command("approver", false, "approve", "-revision", "1", "-sha256", strings.Repeat("0", 64))
	if _, err := store.Read(ctx, credentials["reader"], project, document, 0); !errors.Is(err, teamdocs.ErrNotFound) {
		t.Fatalf("failed approvals must leave the approved head empty: %v", err)
	}
	checkReceipt(command("approver", true, "approve", "-revision", "1", "-sha256", sum), "approver")
	approved, err := store.Read(ctx, credentials["reader"], project, document, 0)
	if err != nil || approved.Content != body || approved.Number != 1 || approved.ApprovedBy != "approver" {
		t.Fatalf("approved head does not match the reviewed revision: %v", err)
	}
	if err := store.Revoke(ctx, project, "reader"); err != nil {
		t.Fatal(err)
	}
	command("reader", false, "read", "-revision", "1")
}
