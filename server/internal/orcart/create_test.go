package orcart_test

import (
	"context"
	"testing"

	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
)

func TestCreateTerminal(t *testing.T) {
	fake := orcarttest.New(t)
	c := &orcart.Client{MetaPath: fake.MetaPath}

	created, err := c.CreateTerminal(context.Background(), "id:wt-1", "my-tab", "mut-1", "")
	if err != nil {
		t.Fatalf("CreateTerminal failed: %v", err)
	}
	if created.Handle == "" {
		t.Errorf("expected non-empty handle, got empty")
	}
	if created.WorktreeID != "wt-1" {
		t.Errorf("expected WorktreeID wt-1, got %s", created.WorktreeID)
	}
	if created.Title != "my-tab" {
		t.Errorf("expected Title my-tab, got %s", created.Title)
	}
	if !created.IsLocal() {
		t.Errorf("expected terminal to be local")
	}
	// The user's default shell: no shell in the request, which an Orca without shell selection would ignore anyway.
	if _, ok := fake.Params("terminal.create")[0]["shell"]; ok {
		t.Errorf("asked for a shell nobody chose: %v", fake.Params("terminal.create")[0])
	}
	if _, err := c.CreateTerminal(context.Background(), "id:wt-1", "bash-tab", "mut-2", orcart.GitBash); err != nil {
		t.Fatalf("CreateTerminal with a shell failed: %v", err)
	}
	if got := fake.Params("terminal.create")[1]["shell"]; got != "git-bash" {
		t.Errorf("shell = %v, want git-bash", got)
	}
}

func TestRenameAndPaneTitle(t *testing.T) {
	fake := orcarttest.New(t)
	c := &orcart.Client{MetaPath: fake.MetaPath}

	created, err := c.CreateTerminal(context.Background(), "id:wt-1", "old-name", "mut-1", "")
	if err != nil {
		t.Fatalf("CreateTerminal failed: %v", err)
	}

	target := created.Target()
	if err := c.RenameTerminal(context.Background(), target, "new-tab-name"); err != nil {
		t.Fatalf("RenameTerminal failed: %v", err)
	}

	if err := c.SetPaneTitle(context.Background(), target, "pane-name"); err != nil {
		t.Fatalf("SetPaneTitle failed: %v", err)
	}
}

// CreateTerminal sends env as terminal.create's env object and a command with shell-ready delivery; an Orca whose schema
// refuses env (invalid_argument, nothing made) is asked once more without it, with the same mutation id, and opens one
// terminal.
func TestCreateTerminalEnvAndCommand(t *testing.T) {
	fake := orcarttest.New(t)
	c := &orcart.Client{MetaPath: fake.MetaPath}
	ctx := context.Background()
	if _, err := c.CreateTerminal(ctx, "id:wt-1", "t", "mut-1", "", orcart.WithEnv(map[string]string{"PERSONA_AGENT": "1"}),
		orcart.WithCommand("cd /tmp/x")); err != nil {
		t.Fatal(err)
	}
	p := fake.Params("terminal.create")[0]
	if env, _ := p["env"].(map[string]any); env["PERSONA_AGENT"] != "1" || len(env) != 1 {
		t.Errorf("env = %v", p["env"])
	}
	if p["command"] != "cd /tmp/x" || p["startupCommandDelivery"] != "shell-ready" {
		t.Errorf("command %v, delivery %v", p["command"], p["startupCommandDelivery"])
	}

	fake.Reset()
	if _, err := c.CreateTerminal(ctx, "id:wt-1", "t", "mut-2", ""); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"env", "command", "startupCommandDelivery"} {
		if _, ok := fake.Params("terminal.create")[0][k]; ok {
			t.Errorf("%s sent though not asked for: %v", k, fake.Params("terminal.create")[0])
		}
	}

	fake.Reset()
	fake.Unknown("terminal.create", "env")
	created, err := c.CreateTerminal(ctx, "id:wt-1", "t", "mut-3", "", orcart.WithEnv(map[string]string{"PERSONA_AGENT": "1"}))
	if err != nil {
		t.Fatal(err)
	}
	creates := fake.Params("terminal.create")
	if len(creates) != 2 || creates[0]["env"] == nil || creates[1]["env"] != nil || creates[1]["clientMutationId"] != "mut-3" {
		t.Fatalf("terminal.create %v", fake.Trace("terminal.create"))
	}
	l, err := c.Terminals(ctx, false)
	if err != nil || created.Handle == "" || len(l.Terminals) != 3 { // mut-1's, mut-2's and this one
		t.Errorf("created %+v; terminals %v, %v", created, l, err)
	}

	// Any other refusal is not retried without env.
	fake.Reset()
	fake.Unknown("terminal.create")
	fake.Handle("terminal.create", func(map[string]any) any { return orcarttest.Fail{Code: "runtime_error"} })
	if _, err := c.CreateTerminal(ctx, "id:wt-1", "t", "mut-4", "", orcart.WithEnv(map[string]string{"PERSONA_AGENT": "1"})); err == nil ||
		fake.Count("terminal.create") != 1 {
		t.Errorf("err %v after %d creates", err, fake.Count("terminal.create"))
	}
}
