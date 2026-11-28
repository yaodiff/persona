package assistant

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// answerAgentDoneBot pairs the bot (pushes on or off) with a web address for the open button, and records posts.
func answerAgentDoneBot(ft *fleetTest, pushes bool) {
	ft.f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0), PublicURL: "https://hub.example",
			Features: FeishuFeatures{Main: true, Cards: true, Sync: true, Pushes: pushes}}, nil
	})
	ft.f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		return map[string]any{"message_id": "om_done"}, nil
	})
}

// driveAgentDone lists sess working and then done: the working → done transition that ends a turn.
func driveAgentDone(ft *fleetTest, sess proto.Session) {
	work := sess
	work.Status = proto.StatusWorking
	done := sess
	done.Status = proto.StatusDone
	ft.sessions("mac", work)
	ft.sessions("mac", done)
}

func waitAgentDonePosts(ft *fleetTest, n int) []map[string]any {
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := ft.f.appParams(AppFeishu, FeishuPost)
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func agentSession() proto.Session {
	return proto.Session{SessionID: "s1", Title: "修登录", Cwd: "/code/app", Agent: "claude", PaneID: "p1",
		LastText: "改好了", UpdatedAt: time.Now()}
}

// A background agent's completed, failed and cut-off turn each gets exactly one Feishu card with the same decision as
// the phone's push: green for done, orange with ⚠️ for a turn error or a model-service cut-off, the result text, the
// machine in the note, the open button, and a reply routes back to that agent (#4674).
func TestFeishuAgentDoneCard(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	for _, tc := range []struct {
		name      string
		lastText  string
		turnErr   string
		wantColor string
		wantBody  string
	}{
		{"completed", "改好了", "", "green", "改好了"},
		{"failed", "改了一半", "exit status 1", "red", "模型服务出错，中断了"},
		{"cut off", "API Error: 529 overloaded", "", "red", "模型服务出错，中断了"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft := newFleetTest(t)
			fastDone(ft)
			answerAgentDoneBot(ft, true)
			sess := agentSession()
			sess.LastText = tc.lastText
			sess.TurnError = tc.turnErr
			driveAgentDone(ft, sess)

			posts := waitAgentDonePosts(ft, 1)
			if len(posts) != 1 {
				t.Fatalf("posted %d cards, want 1", len(posts))
			}
			card, _ := posts[0]["card"].(string)
			for _, want := range []string{`"template":"` + tc.wantColor + `"`, tc.wantBody, "后台 Agent", "Mac", "https://hub.example/s/mac/s1"} {
				if !strings.Contains(card, want) {
					t.Fatalf("card missing %q:\n%s", want, card)
				}
			}
			// The title is the agent's, the state is the header's colour and its tag (no ⚠️), the place the subtitle.
			if !strings.Contains(card, `"title":{"tag":"plain_text","content":"修登录"}`) || strings.Contains(card, "⚠️") ||
				!strings.Contains(card, `"subtitle":{"tag":"plain_text","content":"后台 Agent · Mac"}`) {
				t.Fatalf("card header: %s", card)
			}
			wantTag := map[string]string{"completed": "完成", "failed": "中断", "cut off": "中断"}[tc.name]
			if !strings.Contains(card, `"text":{"tag":"plain_text","content":"`+wantTag+`"}`) {
				t.Fatalf("card lacks the tag %s: %s", wantTag, card)
			}
			fallback, _ := posts[0]["fallback"].(string)
			for _, want := range []string{"修登录", tc.wantBody, "后台 Agent"} {
				if !strings.Contains(fallback, want) {
					t.Fatalf("fallback missing %q: %q", want, fallback)
				}
			}
			// A grey hint says a reply continues the work with this agent (#5438).
			hint := i18n.T("", "assistant-feishu_cards.agent_done_reply_hint")
			if !strings.Contains(card, hint) {
				t.Fatalf("done card missing reply hint %q:\n%s", hint, card)
			}
			if !strings.Contains(fallback, hint) {
				t.Fatalf("done fallback missing reply hint %q: %q", hint, fallback)
			}
			if got := ft.pushesOf(assistantwire.NotifyDone, 1, time.Second); len(got) != 1 {
				t.Fatalf("the phone push changed: %+v", got)
			}
			time.Sleep(50 * time.Millisecond) // rememberBotThread runs right after the post
			// A reply to the card goes to the agent: the card is bound to it.
			if th := ft.s.larkModule().Route("", "om_done"); th.Kind != targetAgent || th.MachineID != "mac" || th.SessionID != "s1" {
				t.Fatalf("the card is not bound to the agent: %+v", th)
			}
			time.Sleep(200 * time.Millisecond) // the same completion settles once: no second card
			if got := ft.f.appParams(AppFeishu, FeishuPost); len(got) != 1 {
				t.Fatalf("posted %d cards, want still 1", len(got))
			}
		})
	}
}

// Nothing is posted when the pushes switch is off, the session is muted, or another Feishu path owns the turn (the
// main conversation, a scheduled task's own conversation, health), or the turn was asked and answered in Feishu.
func TestFeishuAgentDoneGated(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	for _, tc := range []struct {
		name     string
		pushes   bool
		setup    func(ft *fleetTest)
		sess     func() proto.Session
		wantDone int // the phone's NotifyDone pushes still go (or not) as before
	}{
		{"pushes switch off", false, func(ft *fleetTest) {}, agentSession, 1},
		{"muted session", true, func(ft *fleetTest) {
			ft.mutes(Pins{Sessions: []string{"mac/s1"}})
		}, agentSession, 0},
		{"main conversation", true, func(ft *fleetTest) {
			ft.s.fleetTakeSnapshot(Snapshot{Profile: &Profile{MachineID: "mac", Main: "s1"}})
		}, agentSession, 1},
		{"scheduled task conversation", true, func(ft *fleetTest) {
			ft.s.fleetTakeSnapshot(Snapshot{Tasks: []TaskView{{ID: "fu1", Label: "日报", RunMachineID: "mac", RunSessionID: "s1"}}})
		}, agentSession, 1},
		{"health conversation", true, func(ft *fleetTest) {}, func() proto.Session {
			sess := agentSession()
			sess.Space = SpaceHealth
			return sess
		}, 1},
		{"turn asked from Feishu", true, func(ft *fleetTest) {
			ft.s.fleetTakeSnapshot(Snapshot{Channels: &ChannelsReport{Turns: []ChannelTurn{{MachineID: "mac", SessionID: "s1"}}}})
		}, agentSession, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft := newFleetTest(t)
			fastDone(ft)
			answerAgentDoneBot(ft, tc.pushes)
			tc.setup(ft)
			driveAgentDone(ft, tc.sess())
			time.Sleep(400 * time.Millisecond) // the finished delay is 50 ms; a card would have landed by now
			if got := ft.f.appParams(AppFeishu, FeishuPost); len(got) != 0 {
				t.Fatalf("posted %d cards, want none: %+v", len(got), got)
			}
			if got := ft.pushesOf(assistantwire.NotifyDone, 1, 0); len(got) != tc.wantDone {
				t.Fatalf("phone pushes %d, want %d", len(got), tc.wantDone)
			}
		})
	}
}

// A result longer than the card's budget still goes out as one card, marked as cut short.
func TestFeishuAgentDoneLongResult(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	ft := newFleetTest(t)
	fastDone(ft)
	answerAgentDoneBot(ft, true)
	sess := agentSession()
	sess.LastText = strings.Repeat("很长的结果。", 500)
	driveAgentDone(ft, sess)
	posts := waitAgentDonePosts(ft, 1)
	if len(posts) != 1 {
		t.Fatalf("posted %d cards, want 1", len(posts))
	}
	card, _ := posts[0]["card"].(string)
	if !strings.Contains(card, "只发了开头") {
		t.Fatalf("long result is not marked as cut short:\n%s", card)
	}
}

// When the card's JSON size exceeds answerCardMaxBytes and gets halved, the note and fallback
// must mark it as cut short even if the original text was <= agentDoneClip (#4944).
func TestFeishuAgentDoneHalvedCardMarksClipped(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	ft := newFleetTest(t)
	fastDone(ft)
	answerAgentDoneBot(ft, true)
	sess := agentSession()
	sess.Title = strings.Repeat("A", 27000)
	// 600 runes <= agentDoneClip (1000), but with wide title it exceeds answerCardMaxBytes
	// on the first iteration and limit is halved to 500 runes.
	sess.LastText = strings.Repeat("字", 600)
	driveAgentDone(ft, sess)
	posts := waitAgentDonePosts(ft, 1)
	if len(posts) != 1 {
		t.Fatalf("posted %d cards, want 1", len(posts))
	}
	card, _ := posts[0]["card"].(string)
	if !strings.Contains(card, "只发了开头") {
		t.Fatalf("halved card is missing cut short notice:\n%s", card)
	}
	fallback, _ := posts[0]["fallback"].(string)
	if !strings.Contains(fallback, "只发了开头") {
		t.Fatalf("halved fallback is missing cut short notice:\n%s", fallback)
	}
}

// A long result shows its beginning, the rest folded; an agent that asks the owner something is orange; the one
// button opens its conversation, primary.
func TestFeishuAgentDoneLooks(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	ft := newFleetTest(t)
	fastDone(ft)
	answerAgentDoneBot(ft, true)
	sess := agentSession()
	sess.LastText = strings.Repeat("第一段说明。", 60) + "\n\n" + strings.Repeat("第二段细节。", 60)
	driveAgentDone(ft, sess)
	posts := waitAgentDonePosts(ft, 1)
	if len(posts) != 1 {
		t.Fatalf("posted %d cards", len(posts))
	}
	card, _ := posts[0]["card"].(string)
	if !strings.Contains(card, `"tag":"collapsible_panel"`) || !strings.Contains(card, "第二段细节") || strings.Count(card, `"tag":"button"`) != 1 ||
		!strings.Contains(card, `"type":"primary"`) {
		t.Fatalf("card %s", card)
	}
}
