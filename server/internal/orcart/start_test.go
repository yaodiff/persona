package orcart_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
)

// agentSession.agents names the providers Orca runs as chats (Orca 1.4.277's answer, abridged); an older Orca has no
// such method.
func TestChatAgents(t *testing.T) {
	fake := orcarttest.New(t)
	c := &orcart.Client{MetaPath: fake.MetaPath}
	if _, err := c.ChatAgents(context.Background()); !orcart.IsCode(err, "method_not_found") {
		t.Fatalf("an older Orca: %v", err)
	}
	fake.Handle("agentSession.agents", func(map[string]any) any {
		return map[string]any{"agents": []any{
			map[string]any{"agent": "pi", "capabilities": map[string]any{"rewind": false}},
			map[string]any{"agent": "codex", "capabilities": map[string]any{"rewind": true}, "managedRuntime": true},
			map[string]any{"agent": "claude", "capabilities": map[string]any{"rewind": true}, "managedRuntime": true},
		}}
	})
	got, err := c.ChatAgents(context.Background())
	if err != nil || !slices.Equal(got, []string{"pi", "codex", "claude"}) {
		t.Fatalf("agents %v %v", got, err)
	}
}

// agent.launch's refusal of the chat surface is told apart by Orca's words: a missing chat runtime (Orca 1.4.277's
// answer on a computer where it has not installed it), or another reason.
func TestChatRuntimeMissing(t *testing.T) {
	fake := orcarttest.New(t)
	c := &orcart.Client{MetaPath: fake.MetaPath}
	launch := func(message string) error {
		fake.Handle("agent.launch", func(map[string]any) any {
			return orcarttest.Refuse("agent_launch_surface_unavailable", message)
		})
		return c.LaunchChat(context.Background(), "persona-0123456789", orcart.OperationID(time.Now(), "launch"), "id:wt-1",
			"claude", "hi", nil)
	}
	err := launch("Claude can't open as a chat here because its chat runtime isn't installed on this host (install it " +
		"from a new chat tab in Orca). Start it in a terminal instead.")
	if !orcart.ChatSurfaceUnavailable(err) || !orcart.ChatRuntimeMissing(err) {
		t.Fatalf("runtime missing: %v", err)
	}
	err = launch("Claude can't open as a chat here because this workspace runs under WSL. Start it in a terminal instead.")
	if !orcart.ChatSurfaceUnavailable(err) || orcart.ChatRuntimeMissing(err) {
		t.Fatalf("another reason: %v", err)
	}
	if orcart.ChatRuntimeMissing(nil) || orcart.ChatSurfaceUnavailable(nil) {
		t.Fatal("no error")
	}
}
