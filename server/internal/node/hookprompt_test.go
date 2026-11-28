package node

import (
	"testing"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A UserPromptSubmit tells the manager who its prompt is from, never the prompt itself (the apps see the event): a
// meeting's batch and a relay by their message source, a background task's report as one, anything else as the
// user's own words (a pasted block the user wrote included). Other events say nothing of it.
func TestHookEventPromptSource(t *testing.T) {
	batch := prompts.MessageSourceText("New lines", &proto.MessageSource{Kind: proto.SourceMeeting, Title: "Design review"})
	relay := prompts.MessageSourceText("Look at this", &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: "c2", Title: "Plans"})
	for prompt, want := range map[string]string{
		batch:                             "meeting",
		relay:                             "conversation",
		"also open a todo for the cap":    "user",
		prompts.ProtectUserMessage(batch): "user",
		"<task-notification>\n<task-id>a1</task-id>\n<status>completed</status>\n</task-notification>": "notification",
		prompts.RenderSideConversation("also check the flaky parser test"):                             proto.SourceSideConversation,
		`<persona-context kind="side-conversation">separate request</persona-context>do that first`:    proto.SourceSideConversation,
		`<persona-side-conversation />plain side request`:                                              proto.SourceSideConversation,
	} {
		p := hookEventParams(hookInput{HookEventName: "UserPromptSubmit", SessionID: "s1", Prompt: prompt}, "", false)
		if p["prompt_source"] != want {
			t.Errorf("%q: prompt_source %v, want %s", prompt, p["prompt_source"], want)
		}
		if _, ok := p["prompt"]; ok {
			t.Fatal("the prompt went out with the event")
		}
	}
	if p := hookEventParams(hookInput{HookEventName: "Stop", SessionID: "s1", Prompt: "x"}, "", false); p["prompt_source"] != nil {
		t.Fatalf("a Stop says a prompt source: %v", p)
	}
}
