package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// newTextAskService is an assistant with a connected text-only plugin channel "examplechat", ixs pending on the hub
// and already asked in the channel (so a reply answers the latest one). It records the notes sent and the resolutions
// handed to interactions.resolve.
func newTextAskService(t *testing.T, ixs ...proto.Interaction) (*Service, *fakeHub, *[]map[string]any, *[]map[string]any) {
	return newTextAskServiceWithResolve(t, nil, ixs...)
}

// newTextAskServiceWithResolve is newTextAskService with a custom interactions.resolve answer (nil: always ok).
func newTextAskServiceWithResolve(t *testing.T, resolve appAnswer, ixs ...proto.Interaction) (*Service, *fakeHub, *[]map[string]any, *[]map[string]any) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	var notes, resolves []map[string]any
	f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		return map[string]any{"channels": []channel.View{{ID: "examplechat", Name: "Example Chat", Kind: channel.KindIM, Source: channel.SourcePlugin,
			Installed: true, State: channel.StateConnected, Capabilities: channel.Capabilities{Asks: channel.AsksText}}}}, nil
	})
	f.answerApp(AppChannels, ChannelsSend, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		notes = append(notes, m)
		return map[string]any{}, nil
	})
	f.answerApp(AppInteractions, InteractionsResolve, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		resolves = append(resolves, m)
		if resolve != nil {
			return resolve(p)
		}
		return map[string]any{"ok": true}, nil
	})
	f.mu.Lock()
	f.hubCards = append(f.hubCards, ixs...)
	f.sessions["server"] = []proto.Session{{SessionID: "m1", MachineID: "server"}}
	f.mu.Unlock()
	s := newTestService(t, f)
	ctx := context.Background()
	now := time.Now()
	s.chans.mu.Lock()
	s.loadChannelStateLocked(ctx)
	for _, ix := range ixs {
		s.chans.st.Asks = append(s.chans.st.Asks, channel.NewAsk("examplechat", ix, now))
	}
	s.saveChannelStateLocked()
	s.chans.mu.Unlock()
	return s, f, &notes, &resolves
}

// askInState is the channel's pending ask with id ixID as stored now (nil when it was deleted).
func askInState(s *Service, ixID string) *channel.Ask {
	s.chans.mu.Lock()
	defer s.chans.mu.Unlock()
	for _, a := range s.chans.st.Asks {
		if a.ID == ixID {
			cp := *a
			return &cp
		}
	}
	return nil
}

// noteTexts joins the note texts sent to the channel.
func noteTexts(notes *[]map[string]any) string {
	var b strings.Builder
	for _, n := range *notes {
		b.WriteString(n["text"].(string))
		b.WriteString("\n")
	}
	return b.String()
}

// sendOnceText is the text of the session.send_once call the channel made ("" when it made none).
func sendOnceText(f *fakeHub) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	text := ""
	for _, c := range f.calls {
		if c.Method != DeviceSessionSendOnce {
			continue
		}
		var p map[string]any
		_ = json.Unmarshal(c.Params, &p)
		text, _ = p["text"].(string)
	}
	return text
}

// A command the bot does not handle ("/new help me…") leaves the text for the conversation without its name. With an ask
// waiting, that text answers it: the name must not be read as part of the answer, and the message is answered, not
// swallowed or sent on.
func TestChannelReceiveCommandRestAnswersAsk(t *testing.T) {
	for _, c := range []struct {
		name string
		ix   proto.Interaction
		msg  string
		good func(map[string]any) bool
		note string
	}{
		{
			"plan", proto.Interaction{ID: "ix_plan", Kind: proto.IxPlan}, "/new 帮我查下周天气",
			func(r map[string]any) bool { return r["approve"] == false && r["feedback"] == "帮我查下周天气" },
			"已打回，带上了你的话。",
		},
		{
			"question", proto.Interaction{ID: "ix_q", Kind: proto.IxQuestion, Questions: []proto.Question{{Question: "去哪个城市？"}}},
			"/new 北京",
			func(r map[string]any) bool {
				a, _ := r["answers"].(map[string]any)
				return a["去哪个城市？"] == "北京"
			},
			"已回答。",
		},
		{
			// Extra spaces around the rest are trimmed away with the command name.
			"question spaces", proto.Interaction{ID: "ix_q2", Kind: proto.IxQuestion, Questions: []proto.Question{{Question: "去哪个城市？"}}},
			"/new   北京  ",
			func(r map[string]any) bool {
				a, _ := r["answers"].(map[string]any)
				return a["去哪个城市？"] == "北京"
			},
			"已回答。",
		},
		{
			"permission", proto.Interaction{ID: "ix_p", Kind: proto.IxPermission}, "/new 允许",
			func(r map[string]any) bool { return r["allow"] == true },
			"已允许。",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f, notes, resolves := newTextAskService(t, c.ix)
			s.channelReceive("examplechat", channel.Message{ID: "m_" + c.name, Text: c.msg}, "")
			if len(*resolves) != 1 {
				t.Fatalf("%d resolutions, want 1: %v", len(*resolves), *resolves)
			}
			r, _ := (*resolves)[0]["resolution"].(map[string]any)
			if !c.good(r) {
				t.Fatalf("resolution %v still carries the command name", r)
			}
			if len(*notes) != 1 || !strings.Contains((*notes)[0]["text"].(string), c.note) {
				t.Fatalf("confirmation note %v, want %q", *notes, c.note)
			}
			if text := sendOnceText(f); text != "" {
				t.Fatalf("the answer was sent on to the conversation: %q", text)
			}
		})
	}
}

// A message without a command, and one with a command the channel bot does not know (the conversation's own), both
// go on exactly as they were typed.
func TestChannelReceiveNonCommandTextGoesOnUnchanged(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct{ name, msg string }{
		{"plain", "帮我写周报"},
		{"unknown command", "/compact 你好"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f, _, _ := newTextAskService(t) // nothing pending: the message is an ordinary one
			s.channelReceive("examplechat", channel.Message{ID: "m_" + c.name, Text: c.msg}, "")
			text := sendOnceText(f)
			if !strings.Contains(text, c.msg) {
				t.Fatalf("forwarded %q, want it to contain %q", text, c.msg)
			}
		})
	}
	// And channelCommand hands an unknown command back byte for byte.
	s, _, _, _ := newTextAskService(t)
	if handled, rest := s.channelCommand(ctx, "examplechat", channel.Message{ID: "m9", Text: "/compact 你好"}, "/compact 你好"); handled || rest != "/compact 你好" {
		t.Fatalf("unknown command: handled=%v rest=%q", handled, rest)
	}
}

// A resolve that fails on the wire keeps the ask and the answers already given: the last question is asked again (its
// failed answer is dropped), a re-sent reply finishes it, and the ask is deleted only once the resolve goes through.
func TestChannelAnswerKeptOnResolveError(t *testing.T) {
	ix := proto.Interaction{ID: "ix_multi", Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "去哪个城市？"}, {Question: "哪天到？"}}}
	failing := true
	resolve := func(json.RawMessage) (any, error) {
		if failing {
			return nil, errors.New("the hub is restarting")
		}
		return map[string]any{"ok": true}, nil
	}
	s, f, notes, resolves := newTextAskServiceWithResolve(t, resolve, ix)
	recv := func(id, text string) {
		s.channelReceive("examplechat", channel.Message{ID: id, Text: text}, "")
	}

	recv("m1", "北京") // question 1 answered, question 2 is asked next
	recv("m2", "上海") // the final answer: resolve fails
	if a := askInState(s, "ix_multi"); a == nil {
		t.Fatal("the ask was deleted after a failed resolve")
	} else if a.Step != 1 || a.Answers["去哪个城市？"] != "北京" || a.Answers["哪天到？"] != "" {
		t.Fatalf("progress not kept: step=%d answers=%v", a.Step, a.Answers)
	}
	if got := noteTexts(notes); !strings.Contains(got, "刚那条没交上去") {
		t.Fatalf("the failure note does not say to re-send: %q", got)
	}
	if strings.Contains(noteTexts(notes), "还有") {
		t.Fatalf("the failed ask itself was counted as more waiting: %q", noteTexts(notes))
	}
	if text := sendOnceText(f); text != "" {
		t.Fatalf("the failed answer was forwarded as an ordinary message: %q", text)
	}

	failing = false
	recv("m3", "广州") // re-sent for the last question: it resolves and finishes
	if a := askInState(s, "ix_multi"); a != nil {
		t.Fatalf("the ask was kept after a successful resolve: %+v", a)
	}
	if len(*resolves) != 2 {
		t.Fatalf("%d resolves, want 2", len(*resolves))
	}
	answers, _ := (*resolves)[1]["resolution"].(map[string]any)["answers"].(map[string]any)
	if answers["去哪个城市？"] != "北京" || answers["哪天到？"] != "广州" {
		t.Fatalf("the re-sent resolution lost earlier answers: %v", answers)
	}
}

// A single-question ask, a plan and a permission ask have no earlier answers: a failed resolve keeps them at their
// first question / decision, and the re-sent reply goes through.
func TestChannelAnswerKeptOnResolveErrorSingleKinds(t *testing.T) {
	for _, c := range []struct {
		name       string
		ix         proto.Interaction
		first      string
		second     string
		wantSecond func(map[string]any) bool
	}{
		{
			"question", proto.Interaction{ID: "ix_one", Kind: proto.IxQuestion, Questions: []proto.Question{{Question: "去哪个城市？"}}},
			"北京", "上海", func(r map[string]any) bool { return r["answers"].(map[string]any)["去哪个城市？"] == "上海" },
		},
		{
			"plan", proto.Interaction{ID: "ix_plan", Kind: proto.IxPlan},
			"再拆小点", "1", func(r map[string]any) bool { return r["approve"] == true && r["mode"] == "bypassPermissions" },
		},
		{
			"permission", proto.Interaction{ID: "ix_perm", Kind: proto.IxPermission},
			"允许", "允许", func(r map[string]any) bool { return r["allow"] == true },
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			failing := true
			resolve := func(json.RawMessage) (any, error) {
				if failing {
					return nil, errors.New("network down")
				}
				return map[string]any{"ok": true}, nil
			}
			s, _, _, resolves := newTextAskServiceWithResolve(t, resolve, c.ix)
			s.channelReceive("examplechat", channel.Message{ID: "m1", Text: c.first}, "")
			if a := askInState(s, c.ix.ID); a == nil {
				t.Fatal("the ask was deleted after a failed resolve")
			} else if a.Step != 0 || len(a.Answers) != 0 {
				t.Fatalf("a finished ask was not back at its start: step=%d answers=%v", a.Step, a.Answers)
			}
			failing = false
			s.channelReceive("examplechat", channel.Message{ID: "m2", Text: c.second}, "")
			if a := askInState(s, c.ix.ID); a != nil {
				t.Fatal("the ask was kept after a successful resolve")
			}
			r := (*resolves)[1]["resolution"].(map[string]any)
			if !c.wantSecond(r) {
				t.Fatalf("the re-sent resolution is wrong: %v", r)
			}
		})
	}
}

// With another ask waiting in the same channel, a failed resolve names the one still answered by the reply, and the
// "more waiting" line counts only the other ask.
func TestChannelAnswerFailureCountsOnlyOtherAsks(t *testing.T) {
	one := proto.Interaction{ID: "ix_one", Kind: proto.IxPermission}
	two := proto.Interaction{ID: "ix_two", Kind: proto.IxPermission} // asked last: the reply answers this one
	failing := true
	s, _, notes, _ := newTextAskServiceWithResolve(t, func(json.RawMessage) (any, error) {
		if failing {
			return nil, errors.New("network down")
		}
		return map[string]any{"ok": true}, nil
	}, one, two)
	s.channelReceive("examplechat", channel.Message{ID: "m1", Text: "允许"}, "")
	got := noteTexts(notes)
	if !strings.Contains(got, "刚那条没交上去") || !strings.Contains(got, "还有 1 个在等你回复") {
		t.Fatalf("notes should name the failure and one other ask: %q", got)
	}
	if a := askInState(s, "ix_one"); a == nil || askInState(s, "ix_two") == nil {
		t.Fatal("both asks must stay after the failure")
	}
}

// A crash after ReadAnswer saved the final step but before the resolve leaves the question ask at its final step on
// disk: after a restart the reply steps the last question back and finishes it, keeping the earlier answers.
func TestChannelAnswerRecoversFromAFinishedStepOnDisk(t *testing.T) {
	ix := proto.Interaction{ID: "ix_crash", Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "去哪个城市？"}, {Question: "哪天到？"}}}
	s, _, _, resolves := newTextAskService(t, ix)
	s.chans.mu.Lock()
	s.loadChannelStateLocked(context.Background())
	a := func() *channel.Ask {
		for _, x := range s.chans.st.Asks {
			if x.ID == "ix_crash" {
				return x
			}
		}
		return nil
	}()
	a.Step = 2
	a.Answers = map[string]string{"去哪个城市？": "北京", "哪天到？": "上海"} // the lost answer, never resolved
	s.saveChannelStateLocked()
	s.chans.mu.Unlock()

	s.channelReceive("examplechat", channel.Message{ID: "m1", Text: "广州"}, "")
	if got := askInState(s, "ix_crash"); got != nil {
		t.Fatalf("the ask was kept: %+v", got)
	}
	if len(*resolves) != 1 {
		t.Fatalf("%d resolves, want 1", len(*resolves))
	}
	answers, _ := (*resolves)[0]["resolution"].(map[string]any)["answers"].(map[string]any)
	if answers["去哪个城市？"] != "北京" || answers["哪天到？"] != "广州" {
		t.Fatalf("the recovered resolution is wrong: %v", answers)
	}
}

// A successful resolve deletes the ask as before, and the owner's reply is confirmed rather than kept waiting.
func TestChannelAnswerDeletedOnResolveOK(t *testing.T) {
	ix := proto.Interaction{ID: "ix_ok", Kind: proto.IxPermission}
	s, f, notes, resolves := newTextAskService(t, ix)
	s.channelReceive("examplechat", channel.Message{ID: "m1", Text: "允许"}, "")
	if a := askInState(s, "ix_ok"); a != nil {
		t.Fatalf("the ask was kept after a successful resolve: %+v", a)
	}
	if len(*resolves) != 1 || (*resolves)[0]["resolution"].(map[string]any)["allow"] != true {
		t.Fatalf("resolution %v", *resolves)
	}
	if !strings.Contains(noteTexts(notes), "已允许") || sendOnceText(f) != "" {
		t.Fatalf("notes %v forwarded %q", *notes, sendOnceText(f))
	}
}

func TestChannelAnswerStaleOccurrenceCannotResolveReplacement(t *testing.T) {
	firstAt := time.Date(2026, 11, 24, 10, 0, 0, 123456789, time.UTC)
	replacementAt := firstAt.Add(time.Second)
	first := proto.Interaction{ID: "ix_same", Kind: proto.IxPermission, Summary: "first", CreatedAt: firstAt}
	replacement := proto.Interaction{ID: "ix_same", Kind: proto.IxPermission, Summary: "replacement", CreatedAt: replacementAt}
	s, _, _, resolves := newTextAskService(t, replacement)
	s.chans.mu.Lock()
	s.chans.st.Asks = []*channel.Ask{channel.NewAsk("examplechat", first, time.Unix(100, 0))}
	s.saveChannelStateLocked()
	s.chans.mu.Unlock()

	s.channelReceive("examplechat", channel.Message{ID: "m_stale", Text: "允许"}, "act")
	if len(*resolves) != 0 {
		t.Fatalf("the first occurrence's text answer resolved its replacement: %+v", *resolves)
	}
}

func TestChannelAnswerSuccessKeepsSameIDReplacementAsk(t *testing.T) {
	firstAt := time.Date(2026, 11, 24, 10, 0, 0, 123456789, time.UTC)
	replacementAt := firstAt.Add(time.Second)
	first := proto.Interaction{ID: "ix_same", Kind: proto.IxPermission, Summary: "first", CreatedAt: firstAt}
	replacement := proto.Interaction{ID: "ix_same", Kind: proto.IxPermission, Summary: "replacement", CreatedAt: replacementAt}
	started := make(chan struct{})
	release := make(chan struct{})
	resolve := func(p json.RawMessage) (any, error) {
		var params map[string]any
		_ = json.Unmarshal(p, &params)
		if params["created_at"] != firstAt.Format(time.RFC3339Nano) {
			t.Errorf("resolve occurrence = %v, want the first occurrence %s", params["created_at"], firstAt.Format(time.RFC3339Nano))
		}
		close(started)
		<-release
		return map[string]any{"ok": true}, nil
	}
	s, _, notes, _ := newTextAskServiceWithResolve(t, resolve, first)
	done := make(chan struct{})
	go func() {
		s.channelReceive("examplechat", channel.Message{ID: "m_first", Text: "允许"}, "act")
		close(done)
	}()
	<-started
	s.chans.mu.Lock()
	s.chans.st.Asks = append(s.chans.st.Asks, channel.NewAsk("examplechat", replacement, time.Unix(200, 0)))
	s.chans.noteEndedLocked(channel.NewAsk("examplechat", replacement, time.Unix(200, 0)))
	s.saveChannelStateLocked()
	s.chans.mu.Unlock()
	close(release)
	<-done

	kept := askInState(s, "ix_same")
	if kept == nil || !kept.CreatedAt.Equal(replacementAt) {
		t.Fatalf("the first answer removed the replacement ask: %+v", kept)
	}
	s.chans.mu.Lock()
	ended, ok := s.chans.lastEnded["examplechat"]
	s.chans.mu.Unlock()
	if !ok || !ended.ask.CreatedAt.Equal(replacementAt) {
		t.Fatalf("the first answer removed the replacement ended marker: %+v, present=%v", ended, ok)
	}
	if got := noteTexts(notes); !strings.Contains(got, "还有 1 个在等你回复") {
		t.Fatalf("the preserved replacement was not counted as still waiting: %q", got)
	}
}

func TestChannelDelayedEndKeepsNewerLateAnswerMarker(t *testing.T) {
	firstAt := time.Date(2026, 11, 24, 10, 0, 0, 123456789, time.UTC)
	first := proto.Interaction{ID: "ix_same", Kind: proto.IxPermission, CreatedAt: firstAt}
	replacement := first
	replacement.CreatedAt = firstAt.Add(time.Second)
	book := channelsBook{now: func() time.Time { return time.Unix(1000, 0) }}
	book.noteEndedLocked(channel.NewAsk("examplechat", replacement, time.Unix(200, 0)))
	book.noteEndedLocked(channel.NewAsk("examplechat", first, time.Unix(100, 0)))
	ended := book.lastEnded["examplechat"]
	if ended.ask == nil || !ended.ask.CreatedAt.Equal(replacement.CreatedAt) {
		t.Fatalf("an older delayed end replaced the newest late-answer marker: %+v", ended.ask)
	}
}

// -race: while the final resolve marshals the answers unlocked, a concurrent heal must not touch that map.
func TestChannelAnswerResolveInFlightNoMapRace(t *testing.T) {
	ix := proto.Interaction{ID: "ix_multi", Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "去哪个城市？"}, {Question: "哪天到？"}}}
	proceed := make(chan struct{})
	calls := 0
	resolve := func(json.RawMessage) (any, error) {
		if calls++; calls == 1 {
			<-proceed
			return nil, errors.New("network down")
		}
		return map[string]any{"ok": true}, nil
	}
	s, _, _, resolves := newTextAskServiceWithResolve(t, resolve, ix)
	s.channelReceive("examplechat", channel.Message{ID: "m1", Text: "北京"}, "") // Q1 answered; Q2 asked
	// Widen the marshal window (marshal finishes before the gated handler) so the heal overlaps it.
	s.chans.mu.Lock()
	for i := range 2000 {
		s.chans.st.Asks[0].Answers["pad"+strings.Repeat("x", i)] = strings.Repeat("y", 2048)
	}
	s.chans.mu.Unlock()
	// Synchronized only via the channel mutex: as soon as the final step is taken, heal it like a second message.
	healed := make(chan struct{})
	go func() {
		for {
			s.chans.mu.Lock()
			ready := false
			for _, x := range s.chans.st.Asks {
				if x.ID == "ix_multi" && x.Step == len(x.Questions) {
					rewindLastAnswer(x)
					ready = true
				}
			}
			s.chans.mu.Unlock()
			if ready {
				close(healed)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	done := make(chan struct{})
	go func() { s.channelReceive("examplechat", channel.Message{ID: "m2", Text: "上海"}, ""); close(done) }()
	<-healed // the racing heal landed while the first resolve was held mid-flight on proceed
	close(proceed)
	<-done
	// Both rewinds can land; two fresh replies (the final answer dropped) finish the ask: Q1 then Q2.
	s.channelReceive("examplechat", channel.Message{ID: "m3", Text: "广州"}, "")
	s.channelReceive("examplechat", channel.Message{ID: "m4", Text: "天津"}, "")
	if a := askInState(s, "ix_multi"); a != nil {
		t.Fatalf("the ask was not finished by the replacement answers: %+v", a)
	}
	answers := (*resolves)[len(*resolves)-1]["resolution"].(map[string]any)["answers"].(map[string]any)
	if answers["去哪个城市？"] != "广州" || answers["哪天到？"] != "天津" {
		t.Fatalf("replacement resolution is wrong: %v", answers)
	}
}

// channelServiceForKind is a connected plugin channel and an existing main conversation. Replies sent through the
// ordinary extension sink are recorded in sent.
func channelServiceForKind(t *testing.T, kind channel.Kind) (*Service, *fakeHub, *[]map[string]any) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	f.setSessions("server", proto.Session{SessionID: "m1", MachineID: "server", Assistant: true})
	var sent []map[string]any
	f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		return map[string]any{"channels": []channel.View{{ID: "examplechat", Name: "Example Chat", Kind: kind,
			Source: channel.SourcePlugin, Installed: true, State: channel.StateConnected,
			Capabilities: channel.Capabilities{MaxText: 4000}}}}, nil
	})
	f.answerApp(AppChannels, ChannelsSend, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		sent = append(sent, m)
		return map[string]any{}, nil
	})
	return newTestService(t, f), f, &sent
}

// A mention has to opt into main explicitly. An IM channel keeps its existing default, the owner's main chat (#11130).
func TestMentionChannelDefaultsToNewChatWithoutMainOptIn(t *testing.T) {
	for _, kind := range []channel.Kind{channel.KindIM, channel.KindMention} {
		t.Run(string(kind), func(t *testing.T) {
			s, _, _ := channelServiceForKind(t, kind)
			rt, err := s.channelTarget(context.Background(), "examplechat")
			if err != nil {
				t.Fatal(err)
			}
			if kind == channel.KindIM {
				if !rt.main || rt.isNew || rt.session.SessionID != "m1" {
					t.Fatalf("the IM default changed: %+v", rt)
				}
				return
			}
			if rt.main || !rt.isNew || rt.session.SessionID == "m1" {
				t.Fatalf("a mention without target opted into main: %+v", rt)
			}
		})
	}
}

// An owner's mention request uses the ordinary target setting and the same main turn and extension answer path as IM.
// The fixture is raw JSON on purpose: an old host silently drops fields it does not implement, which this test catches.
func TestMentionChannelMainTurnCarriesOriginContextAndPublicCaution(t *testing.T) {
	s, f, sent := channelServiceForKind(t, channel.KindMention)
	ctx := context.Background()
	v := channel.View{ID: "examplechat", Name: "Example Chat", Kind: channel.KindMention, Source: channel.SourcePlugin}
	if err := s.updateChannel(ctx, v, nil, &channel.Target{Kind: channel.TargetMain}, nil); err != nil {
		t.Fatalf("target {kind: main}: %v", err)
	}
	var m channel.Message
	raw := []byte(`{"id":"om_request","text":"What changed?","owner":true,"chat":"Release chat","topic":"Release 1.3",` +
		`"quote":"We ship on Friday.","context":"09:00 Alice: The migration is done.\n09:01 Bob: Docs are ready."}`)
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if err := s.channelForward(ctx, "examplechat", m); err != nil {
		t.Fatalf("the owner's mention did not enter the assistant pipeline: %v", err)
	}
	f.mu.Lock()
	calls := append([]map[string]any(nil), f.sentOnce...)
	f.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("%d main sends, want one: %+v", len(calls), calls)
	}
	p := calls[0]
	if p["session_id"] != "m1" || p["client_msg_id"] != "examplechat-om_request" || p["public"] != true {
		t.Fatalf("main/public send params: %+v", p)
	}
	text, _ := p["text"].(string)
	for _, want := range []string{"Release chat", "Release 1.3", "We ship on Friday.", "The migration is done.", "Docs are ready.", "What changed?",
		"其他对话", "其他会话", "邮件", "健康", "明确"} {
		if !strings.Contains(text, want) {
			t.Errorf("the main mention turn lost %q:\n%s", want, text)
		}
	}
	runs := s.channelRuns("examplechat")
	if len(runs) != 1 || runs[0].SessionID != "m1" || runs[0].ReplyTo != "om_request" {
		t.Fatalf("main run: %+v", runs)
	}
	waitUntil(t, func() bool {
		_, live := s.channelRunByID(runs[0].ID)
		return live != nil
	})
	s.finishChannelRun(&runs[0], &channel.Result{Answer: "Only the documentation changed."})
	if len(*sent) != 1 || (*sent)[0]["text"] != "Only the documentation changed." || (*sent)[0]["kind"] != channel.OutAnswer ||
		(*sent)[0]["reply_to"] != "om_request" {
		t.Fatalf("extension answers: %+v", *sent)
	}
}

// Mention runs share the main conversation, not a reply destination. Two public topics must keep independent runs so
// each answer returns to its own request; the existing same-channel IM folding control above is unchanged (#11130).
func TestMentionChannelKeepsDistinctTopicsAsDistinctRuns(t *testing.T) {
	s, _, _ := channelServiceForKind(t, channel.KindMention)
	ctx := context.Background()
	v := channel.View{ID: "examplechat", Name: "Example Chat", Kind: channel.KindMention, Source: channel.SourcePlugin}
	if err := s.updateChannel(ctx, v, nil, &channel.Target{Kind: channel.TargetMain}, nil); err != nil {
		t.Fatal(err)
	}
	owner := true
	for _, m := range []channel.Message{
		{ID: "om_a", Text: "first", Owner: &owner, Chat: "chat A", Topic: "topic A"},
		{ID: "om_b", Text: "second", Owner: &owner, Chat: "chat B", Topic: "topic B"},
	} {
		if err := s.channelForward(ctx, "examplechat", m); err != nil {
			t.Fatal(err)
		}
	}
	runs := s.channelRuns("examplechat")
	if len(runs) != 2 {
		t.Fatalf("distinct public topics folded into one reply destination: %+v", runs)
	}
	if runs[0].ReplyTo != "om_a" || runs[1].ReplyTo != "om_b" {
		t.Fatalf("mention reply destinations: %+v", runs)
	}
}

// Asking a public turn for its missing answer is still the same public turn, never a private owner turn (#11130).
func TestMentionChannelAskAgainStaysPublic(t *testing.T) {
	s, f, _ := channelServiceForKind(t, channel.KindMention)
	ctx := context.Background()
	v := channel.View{ID: "examplechat", Name: "Example Chat", Kind: channel.KindMention, Source: channel.SourcePlugin}
	if err := s.updateChannel(ctx, v, nil, &channel.Target{Kind: channel.TargetMain}, nil); err != nil {
		t.Fatal(err)
	}
	owner := true
	if err := s.channelForward(ctx, "examplechat", channel.Message{ID: "om_public", Text: "question", Owner: &owner, Chat: "chat"}); err != nil {
		t.Fatal(err)
	}
	var r *channel.Run
	var live *chanLive
	waitUntil(t, func() bool {
		r, live = s.channelRunByID("examplechat_om_public")
		return live != nil
	})
	if !s.askAgain(ctx, r, live, 0) {
		t.Fatal("the public turn was not asked again")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sentOnce) != 2 || f.sentOnce[1]["public"] != true {
		t.Fatalf("ask-again lost the public marker: %+v", f.sentOnce)
	}
}

// channel.receive remains owner-only for the main-chat path. Someone else's request belongs to the extension's guest
// conversation (whose existing main:true rejection is covered by TestGuestConversationRefusals), never to main (#11130).
func TestMentionChannelReceiveRefusesNonOwnerForMain(t *testing.T) {
	s, f, sent := channelServiceForKind(t, channel.KindMention)
	ctx := context.Background()
	v := channel.View{ID: "examplechat", Name: "Example Chat", Kind: channel.KindMention, Source: channel.SourcePlugin}
	if err := s.updateChannel(ctx, v, nil, &channel.Target{Kind: channel.TargetMain}, nil); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"id":"om_guest","text":"Please answer for the owner","owner":false,"chat":"Release chat"}`)
	if err := s.ChannelReceive(ctx, ChannelReceive{Channel: "examplechat", Message: raw}); err == nil {
		t.Fatal("a non-owner mention was accepted by the owner-only main receive path")
	}
	f.mu.Lock()
	starts, sends := len(f.sends), len(f.sentOnce)
	f.mu.Unlock()
	if starts != 0 || sends != 0 || len(*sent) != 0 || len(s.channelRuns("examplechat")) != 0 {
		t.Fatalf("a non-owner reached a conversation: starts=%d sends=%d replies=%v runs=%v", starts, sends, *sent, s.channelRuns("examplechat"))
	}
}

// Receipt is not turn completion, and a busy main keeps the existing queue/merge behavior. This IM control must stay
// green: the mention opt-in changes neither acknowledgment nor run folding for existing channels (#11130).
func TestChannelReceiveAcknowledgesBeforeBusyMainTurnAndFolds(t *testing.T) {
	s, f, sent := channelServiceForKind(t, channel.KindIM)
	started := make(chan struct{})
	release := make(chan struct{})
	f.answerNode(DeviceSessionSendOnce, func(_ string, raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		if p["client_msg_id"] == "examplechat-m1" {
			close(started)
			<-release
		}
		return map[string]any{}, nil
	})
	ctx := context.Background()
	if err := s.ChannelReceive(ctx, ChannelReceive{Channel: "examplechat", Message: []byte(`{"id":"m1","text":"first"}`)}); err != nil {
		t.Fatalf("first receive did not acknowledge acceptance: %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first turn did not reach main")
	}
	if err := s.ChannelReceive(ctx, ChannelReceive{Channel: "examplechat", Message: []byte(`{"id":"m2","text":"second"}`)}); err != nil {
		t.Fatalf("second receive did not acknowledge while main was busy: %v", err)
	}
	close(release)
	waitUntil(t, func() bool {
		runs := s.channelRuns("examplechat")
		if len(runs) != 1 || len(runs[0].MsgIDs) != 2 {
			return false
		}
		// Only a watched run is answered (checkChannelRun): the folded run's watcher registers it on its own goroutine.
		_, watched := s.channelRunByID(runs[0].ID)
		return watched != nil
	})
	runs := s.channelRuns("examplechat")
	if runs[0].ReplyTo != "m2" || len(runs[0].Folded) != 1 || runs[0].Folded[0].ReplyTo != "m1" || len(*sent) != 0 {
		t.Fatalf("busy main run/replies: %+v / %+v", runs[0], *sent)
	}
	s.finishChannelRun(&runs[0], &channel.Result{Answer: "one answer"})
	if len(*sent) != 1 || (*sent)[0]["reply_to"] != "m2" {
		t.Fatalf("busy main answers: %+v", *sent)
	}
}

// A picture over the per-image limit (a photo sent as the original) reaches the conversation scaled down to fit,
// instead of being dropped as "additional images"; one that can't be decoded is named as too large (#11985).
func TestChannelReceiveScalesDownALargePicture(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1100, 1100))
	r := rand.New(rand.NewPCG(3, 4))
	for i := range img.Pix {
		img.Pix[i] = byte(r.Uint32())
	}
	for i := 3; i < len(img.Pix); i += 4 { // opaque: it comes back as a JPEG
		img.Pix[i] = 0xff
	}
	var big bytes.Buffer
	if err := png.Encode(&big, img); err != nil {
		t.Fatal(err)
	}
	if big.Len() <= channel.ImageMost {
		t.Fatalf("the source is only %d bytes", big.Len())
	}
	s, f, _, _ := newTextAskService(t)
	f.mu.Lock()
	f.hello.Locale = "zh-Hans"
	f.mu.Unlock()
	s.channelReceive("examplechat", channel.Message{ID: "m_big", Text: "看看这张图", Images: []channel.Blob{
		{MIME: "image/png", Data: big.Bytes()},
		{MIME: "image/png", Data: bytes.Repeat([]byte("x"), channel.ImageMost+1)},
	}}, "")
	var p struct {
		Text   string        `json:"text"`
		Images []proto.Image `json:"images"`
	}
	f.mu.Lock()
	for _, c := range f.calls {
		if c.Method == DeviceSessionSendOnce {
			_ = json.Unmarshal(c.Params, &p)
		}
	}
	f.mu.Unlock()
	if len(p.Images) != 1 || p.Images[0].Mime != "image/jpeg" {
		t.Fatalf("sent %d images, the first %q", len(p.Images), func() string {
			if len(p.Images) == 0 {
				return ""
			}
			return p.Images[0].Mime
		}())
	}
	data, err := base64.StdEncoding.DecodeString(p.Images[0].Data)
	if err != nil || len(data) > channel.ImageMost {
		t.Fatalf("sent %d bytes: %v", len(data), err)
	}
	tooLarge := i18n.T(i18n.Lang("zh-Hans"), "assistant-channels_plugin.image_too_large")
	extra := i18n.T(i18n.Lang("zh-Hans"), "assistant-channels_plugin.extra_images")
	if !strings.Contains(p.Text, tooLarge) || strings.Contains(p.Text, extra) {
		t.Fatalf("sent text %q", p.Text)
	}
}
