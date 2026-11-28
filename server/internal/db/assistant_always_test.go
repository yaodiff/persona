package db

import (
	"context"
	"strings"
	"testing"
)

// "Always allow in this conversation" on an assistant conversation: a card is allowed only when every key it names was granted there.
func TestAssistantAlways(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	long := "codex-command:" + strings.Repeat("printf x; ", 2000) // whole commands are keys: longer than an index entry
	if err := d.AllowAlways(ctx, "s1", "Bash", []string{"tool:Bash", long}); err != nil {
		t.Fatal(err)
	}
	if err := d.AllowAlways(ctx, "s1", "Bash", []string{"tool:Bash"}); err != nil { // again: no error
		t.Fatal(err)
	}
	for _, c := range []struct {
		session string
		keys    []string
		want    bool
	}{
		{"s1", []string{"tool:Bash"}, true},
		{"s1", []string{long, "tool:Bash", "tool:Bash"}, true},
		{"s1", []string{"tool:Bash", "tool:Edit"}, false}, // one not granted
		{"s2", []string{"tool:Bash"}, false},              // another conversation
		{"s1", nil, false},
		{"", []string{"tool:Bash"}, false},
	} {
		got, err := d.AlwaysAllowed(ctx, c.session, c.keys)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("AlwaysAllowed(%q, %d keys) = %v, want %v", c.session, len(c.keys), got, c.want)
		}
	}
}

func TestAssistantAlwaysListAndRevoke(t *testing.T) {
	d, ctx := open(t), context.Background()
	for _, session := range []string{"s1", "s2"} {
		if err := d.AllowAlways(ctx, session, "Bash", []string{"tool:Bash", "tool:Edit"}); err != nil {
			t.Fatal(err)
		}
	}
	grants, err := d.AlwaysGrants(ctx, "s1")
	if err != nil || len(grants) != 2 {
		t.Fatalf("list: %+v, %v", grants, err)
	}
	for _, g := range grants {
		if len(g.KeyHash) != 64 || g.ToolName != "Bash" || g.CreatedAt.IsZero() {
			t.Fatalf("grant: %+v", g)
		}
	}
	for range 2 {
		if err := d.RevokeAlways(ctx, "s1", alwaysKeyHash("tool:Bash")); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		session, key string
		want         bool
	}{{"s1", "tool:Bash", false}, {"s1", "tool:Edit", true}, {"s2", "tool:Bash", true}} {
		got, err := d.AlwaysAllowed(ctx, c.session, []string{c.key})
		if err != nil || got != c.want {
			t.Fatalf("%+v: %v, %v", c, got, err)
		}
	}
	grants, err = d.AlwaysGrants(ctx, "missing")
	if err != nil || grants == nil || len(grants) != 0 {
		t.Fatalf("empty list: %+v, %v", grants, err)
	}
}

func TestAssistantAlwaysCopy(t *testing.T) {
	d, ctx := open(t), context.Background()
	if err := d.AllowAlways(ctx, "old-main", "Bash", []string{"tool:Bash", "tool:Edit"}); err != nil {
		t.Fatal(err)
	}
	if err := d.AllowAlways(ctx, "new-main", "Read", []string{"tool:Read"}); err != nil {
		t.Fatal(err)
	}
	copied, err := d.CopyAlways(ctx, "old-main", "new-main")
	if err != nil || copied != 2 {
		t.Fatalf("CopyAlways = (%d, %v), want (2, nil)", copied, err)
	}
	// Idempotent: repeating does not fail or insert duplicates.
	if again, err := d.CopyAlways(ctx, "old-main", "new-main"); err != nil || again != 0 {
		t.Fatalf("repeat CopyAlways = (%d, %v), want (0, nil)", again, err)
	}
	for _, key := range []string{"tool:Bash", "tool:Edit", "tool:Read"} {
		if ok, err := d.AlwaysAllowed(ctx, "new-main", []string{key}); err != nil || !ok {
			t.Fatalf("new-main missing %q: ok=%v err=%v", key, ok, err)
		}
	}
	// Revoking on new-main removes it there without touching old-main.
	if err := d.RevokeAlways(ctx, "new-main", alwaysKeyHash("tool:Bash")); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.AlwaysAllowed(ctx, "new-main", []string{"tool:Bash"}); err != nil || ok {
		t.Fatalf("new-main still allows revoked key: ok=%v err=%v", ok, err)
	}
	if ok, err := d.AlwaysAllowed(ctx, "old-main", []string{"tool:Bash"}); err != nil || !ok {
		t.Fatalf("old-main lost grant after new-main revoke: ok=%v err=%v", ok, err)
	}
	for _, pair := range [][2]string{{"", "new-main"}, {"old-main", ""}, {"same", "same"}} {
		if n, err := d.CopyAlways(ctx, pair[0], pair[1]); err != nil || n != 0 {
			t.Fatalf("CopyAlways(%q, %q) = (%d, %v), want (0, nil)", pair[0], pair[1], n, err)
		}
	}
}
