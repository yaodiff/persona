package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestFeishuInteractionActionsCarryOccurrence(t *testing.T) {
	needsLarkLayer(t)
	ft := newFleetTestNoStore(t)
	at := time.Date(2026, 11, 21, 1, 2, 3, 456789123, time.UTC)
	honored := true
	card := ft.s.interactionCard(context.Background(), proto.Interaction{
		ID: "ix_feishu_occurrence", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission,
		CreatedAt: at, CanClarify: true, AlwaysLabel: "Always", AlwaysHonored: &honored,
	})
	var raw any
	if err := json.Unmarshal([]byte(card.JSON()), &raw); err != nil {
		t.Fatal(err)
	}
	actions := 0
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if v["a"] == "ix" {
				actions++
				if v["created_at"] != at.Format(time.RFC3339Nano) {
					t.Errorf("interaction action lacks its occurrence: %+v", v)
				}
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(raw)
	if actions < 4 {
		t.Fatalf("fixture found only %d interaction actions", actions)
	}
}

func TestChannelAnnouncementClearsOnlyDifferentOccurrenceEnd(t *testing.T) {
	ft := newFleetTestNoStore(t)
	first := proto.Interaction{ID: "ix_channel_occurrence_end", MachineID: "mac", SessionID: "s1",
		Kind: proto.IxPermission, CreatedAt: time.Now().UTC()}
	ft.s.InteractionEnded(context.Background(), InteractionEnded{Interaction: first})
	if !ft.s.interactionEnded(first.ID) {
		t.Fatal("the first occurrence did not record its terminal marker")
	}
	replacement := first
	replacement.CreatedAt = first.CreatedAt.Add(time.Second)
	ft.s.InteractionAnnounced(context.Background(), InteractionAnnounced{Interaction: replacement})
	if ft.s.interactionEnded(first.ID) {
		t.Fatal("the earlier occurrence's terminal marker blocked its replacement")
	}
	ft.s.InteractionEnded(context.Background(), InteractionEnded{Interaction: first})
	ft.s.InteractionAnnounced(context.Background(), InteractionAnnounced{Interaction: first})
	if !ft.s.interactionEnded(first.ID) {
		t.Fatal("a duplicate announcement erased the same occurrence's in-flight terminal guard")
	}
}

func TestEarlierOccurrenceEndKeepsReplacementAnnouncement(t *testing.T) {
	ft := newFleetTestNoStore(t)
	first := proto.Interaction{ID: "ix_channel_replacement_in_flight", MachineID: "mac", SessionID: "s1",
		Kind: proto.IxPermission, CreatedAt: time.Now().UTC()}
	replacement := first
	replacement.CreatedAt = first.CreatedAt.Add(time.Second)
	ft.s.InteractionAnnounced(context.Background(), InteractionAnnounced{Interaction: replacement})
	ft.s.chans.mu.Lock()
	ft.s.chans.stLoaded = true
	ft.s.chans.st.Asks = []*channel.Ask{channel.NewAsk("ch", replacement, time.Now())}
	ft.s.chans.mu.Unlock()
	ft.s.InteractionEnded(context.Background(), InteractionEnded{Interaction: first})
	ft.s.chans.mu.Lock()
	announced := ft.s.chans.announced[replacement.ID]
	asks := len(ft.s.chans.st.Asks)
	ft.s.chans.mu.Unlock()
	if !announced {
		t.Fatal("the earlier occurrence's delayed terminal event removed the replacement's announcement")
	}
	if asks != 1 {
		t.Fatal("the earlier occurrence's delayed terminal event closed the replacement's text ask")
	}
}

func TestFeishuReplacementPostsAfterEarlierOccurrenceEnds(t *testing.T) {
	needsLarkLayer(t)
	e := newThreadEnv(t, nil)
	first := proto.Interaction{ID: "ix_feishu_delayed_old_end", Kind: proto.IxPermission, MachineID: "server", SessionID: "m1",
		ToolName: "Bash", CreatedAt: time.Now().UTC()}
	replacement := first
	replacement.CreatedAt = first.CreatedAt.Add(time.Second)
	e.s.chans.mu.Lock()
	e.s.chans.announced = map[string]bool{replacement.ID: true}
	e.s.chans.mu.Unlock()
	e.s.InteractionEnded(context.Background(), InteractionEnded{Interaction: first})
	e.s.postInteractionCard(context.Background(), replacement)
	posted := false
	for _, p := range e.f.appParams(AppFeishu, FeishuPost) {
		card, _ := p["card"].(string)
		if strings.Contains(card, replacement.CreatedAt.Format(time.RFC3339Nano)) {
			posted = true
		}
	}
	if !posted {
		t.Fatal("the earlier occurrence's terminal marker suppressed the replacement's Feishu card")
	}
}

func TestFeishuReplacementReplacesEarlierLedger(t *testing.T) {
	needsLarkLayer(t)
	e := newThreadEnv(t, nil)
	first := proto.Interaction{ID: "ix_feishu_old_ledger", Kind: proto.IxPermission, MachineID: "server", SessionID: "m1",
		ToolName: "Bash", CreatedAt: time.Now().UTC()}
	replacement := first
	replacement.CreatedAt = first.CreatedAt.Add(time.Second)
	keepChatCard(e.s, first.ID, &botCard{MessageID: "om_first", Ix: first})
	e.s.postInteractionCard(context.Background(), replacement)
	posted := false
	for _, p := range e.f.appParams(AppFeishu, FeishuPost) {
		card, _ := p["card"].(string)
		if strings.Contains(card, replacement.CreatedAt.Format(time.RFC3339Nano)) {
			posted = true
		}
	}
	if !posted {
		t.Fatal("the earlier occurrence's ledger entry suppressed the replacement's Feishu card")
	}
	got, _ := chatCard(e.s, first.ID)
	if got.MessageID == "" || !got.Ix.CreatedAt.Equal(replacement.CreatedAt) {
		t.Fatalf("the replacement did not replace the old ledger occurrence: %+v", got)
	}
}

func TestChannelsCatchUpAnnouncesReplacementOccurrence(t *testing.T) {
	needsLarkLayer(t)
	st := &memStore{}
	f, posts := feishuHub("ou_a", time.Unix(100, 0))
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	s := newServiceOn(t, f, st)
	first := proto.Interaction{ID: "ix_channel_catchup_replacement", Kind: proto.IxPermission, MachineID: "server", SessionID: "m1",
		ToolName: "Bash", CreatedAt: time.Now().UTC()}
	replacement := first
	replacement.CreatedAt = first.CreatedAt.Add(time.Second)
	s.InteractionAnnounced(context.Background(), InteractionAnnounced{Interaction: first})
	waitUntil(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, p := range *posts {
			card, _ := p["card"].(string)
			if strings.Contains(card, first.CreatedAt.Format(time.RFC3339Nano)) {
				return true
			}
		}
		return false
	})
	s.chans.mu.Lock()
	s.chans.connected = true
	s.chans.mu.Unlock()
	f.hubCards = []proto.Interaction{replacement}
	s.channelsCatchUp(context.Background())
	waitUntil(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, p := range *posts {
			card, _ := p["card"].(string)
			if strings.Contains(card, replacement.CreatedAt.Format(time.RFC3339Nano)) {
				return true
			}
		}
		return false
	})
}

func TestCatchUpCannotDowngradeNewerAnnouncement(t *testing.T) {
	needsLarkLayer(t)
	f, posts := feishuHub("ou_a", time.Unix(100, 0))
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	s := newServiceOn(t, f, &memStore{})
	first := proto.Interaction{ID: "ix_channel_stale_catchup", Kind: proto.IxPermission, MachineID: "server", SessionID: "m1",
		ToolName: "Bash", CreatedAt: time.Now().UTC()}
	replacement := first
	replacement.CreatedAt = first.CreatedAt.Add(time.Second)
	s.InteractionAnnounced(context.Background(), InteractionAnnounced{Interaction: replacement})
	waitUntil(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, p := range *posts {
			card, _ := p["card"].(string)
			if strings.Contains(card, replacement.CreatedAt.Format(time.RFC3339Nano)) {
				return true
			}
		}
		return false
	})
	s.chans.mu.Lock()
	s.chans.connected = true
	s.chans.mu.Unlock()
	f.hubCards = []proto.Interaction{first} // a delayed reconnect snapshot
	s.channelsCatchUp(context.Background())
	s.chans.mu.Lock()
	announcedAt := s.chans.announcedOccurrence[first.ID]
	s.chans.mu.Unlock()
	if !announcedAt.Equal(replacement.CreatedAt) {
		t.Fatalf("stale catch-up downgraded the live announcement: got %s want %s", announcedAt, replacement.CreatedAt)
	}
}

func TestOlderInFlightPostCannotReplaceNewerLedger(t *testing.T) {
	needsLarkLayer(t)
	e := newThreadEnv(t, nil)
	first := proto.Interaction{ID: "ix_feishu_in_flight_order", Kind: proto.IxPermission, MachineID: "server", SessionID: "m1",
		ToolName: "Bash", CreatedAt: time.Now().UTC()}
	replacement := first
	replacement.CreatedAt = first.CreatedAt.Add(time.Second)
	entered, release := make(chan struct{}), make(chan struct{})
	e.s.postCardHook = func(ix proto.Interaction) {
		if ix.ID == first.ID && ix.CreatedAt.Equal(first.CreatedAt) {
			close(entered)
			<-release
		}
	}
	t.Cleanup(func() { e.s.postCardHook = nil })
	done := make(chan struct{})
	go func() {
		e.s.postInteractionCard(context.Background(), first)
		close(done)
	}()
	<-entered
	e.s.chans.mu.Lock()
	e.s.chans.announced = map[string]bool{replacement.ID: true}
	e.s.chans.announcedOccurrence = map[string]time.Time{replacement.ID: replacement.CreatedAt}
	e.s.chans.mu.Unlock()
	e.s.postInteractionCard(context.Background(), replacement)
	postedReplacement, _ := chatCard(e.s, replacement.ID)
	mid := postedReplacement.MessageID
	close(release)
	<-done
	current, _ := chatCard(e.s, replacement.ID)
	if mid == "" || current.MessageID != mid || !current.Ix.CreatedAt.Equal(replacement.CreatedAt) {
		t.Fatalf("the older in-flight post displaced the replacement: before=%q after=%+v", mid, current)
	}
	for _, p := range e.f.appParams(AppFeishu, FeishuUpdateCard) {
		if p["message_id"] == mid {
			t.Fatalf("the older in-flight post closed the replacement message %s", mid)
		}
	}
}

func TestEarlierAnnouncementCannotReplaceCurrentOccurrence(t *testing.T) {
	ft := newFleetTestNoStore(t)
	first := proto.Interaction{ID: "ix_channel_stale_announcement", MachineID: "mac", SessionID: "s1",
		Kind: proto.IxPermission, CreatedAt: time.Now().UTC()}
	replacement := first
	replacement.CreatedAt = first.CreatedAt.Add(time.Second)
	ft.s.InteractionAnnounced(context.Background(), InteractionAnnounced{Interaction: replacement})
	ft.s.InteractionAnnounced(context.Background(), InteractionAnnounced{Interaction: first})
	ft.s.chans.mu.Lock()
	announcedAt := ft.s.chans.announcedOccurrence[first.ID]
	ft.s.chans.mu.Unlock()
	if !announcedAt.Equal(replacement.CreatedAt) {
		t.Fatalf("the older announcement replaced the current occurrence: got %s want %s", announcedAt, replacement.CreatedAt)
	}
	if ft.s.interactionEndedFor(replacement) {
		t.Fatal("the older announcement made the current Feishu post look superseded")
	}
}

func TestEarlierTerminalCannotDowngradeCurrentEndMarker(t *testing.T) {
	ft := newFleetTestNoStore(t)
	first := proto.Interaction{ID: "ix_channel_terminal_regression", MachineID: "mac", SessionID: "s1",
		Kind: proto.IxPermission, CreatedAt: time.Now().UTC()}
	replacement := first
	replacement.CreatedAt = first.CreatedAt.Add(time.Second)
	ft.s.InteractionAnnounced(context.Background(), InteractionAnnounced{Interaction: replacement})
	ft.s.InteractionEnded(context.Background(), InteractionEnded{Interaction: replacement})
	ft.s.InteractionEnded(context.Background(), InteractionEnded{Interaction: first})
	ft.s.chans.mu.Lock()
	endedAt := ft.s.chans.endedOccurrence[first.ID]
	ft.s.chans.mu.Unlock()
	if !endedAt.Equal(replacement.CreatedAt) {
		t.Fatalf("the older terminal event downgraded the end marker: got %s want %s", endedAt, replacement.CreatedAt)
	}
	if !ft.s.interactionEndedFor(replacement) {
		t.Fatal("the current occurrence lost its in-flight terminal guard")
	}
}

func TestCatchUpClosesEarlierBotCardWithPendingReplacement(t *testing.T) {
	needsLarkLayer(t)
	e := newThreadEnv(t, nil)
	first := proto.Interaction{ID: "ix_catchup_bot_occurrence", Kind: proto.IxPermission, MachineID: "server", SessionID: "m1",
		ToolName: "Bash", CreatedAt: time.Now().UTC()}
	replacement := first
	replacement.CreatedAt = first.CreatedAt.Add(time.Second)
	e.f.withQueue(replacement)
	keepChatCard(e.s, first.ID, &botCard{MessageID: "om_first", Ix: first})
	e.s.larkModule().AsksCatchUp(context.Background(), map[string]proto.Interaction{first.ID: replacement})
	waitFor(t, "the earlier Feishu card to close with a pending replacement", func() bool {
		for _, p := range e.f.appParams(AppFeishu, FeishuUpdateCard) {
			if p["message_id"] == "om_first" {
				return true
			}
		}
		return false
	})
}

func TestCatchUpRemovesEarlierTextAskWithPendingReplacement(t *testing.T) {
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	s := newServiceOn(t, f, &memStore{})
	first := proto.Interaction{ID: "ix_catchup_text_occurrence", Kind: proto.IxPermission, MachineID: "server", SessionID: "m1",
		ToolName: "Bash", CreatedAt: time.Now().UTC()}
	replacement := first
	replacement.CreatedAt = first.CreatedAt.Add(time.Second)
	s.chans.mu.Lock()
	s.chans.stLoaded = true
	s.chans.st.Asks = []*channel.Ask{channel.NewAsk("ch", first, time.Now())}
	s.chans.mu.Unlock()
	f.hubCards = []proto.Interaction{replacement}
	s.channelsCatchUp(context.Background())
	s.chans.mu.Lock()
	asks := len(s.chans.st.Asks)
	s.chans.mu.Unlock()
	if asks != 0 {
		t.Fatal("the pending same-ID replacement kept the earlier text ask alive")
	}
}

func TestEarlierOccurrenceCannotDeleteReplacementBotCard(t *testing.T) {
	needsLarkLayer(t)
	ft := newFleetTestNoStore(t)
	first := proto.Interaction{ID: "ix_bot_ledger_occurrence", MachineID: "mac", SessionID: "s1",
		Kind: proto.IxPermission, CreatedAt: time.Now().UTC()}
	replacement := first
	replacement.CreatedAt = first.CreatedAt.Add(time.Second)
	keepChatCard(ft.s, first.ID, &botCard{MessageID: "om_replacement", Ix: replacement})
	ft.s.feishuInteractionDone(first, "ended")
	got, _ := chatCard(ft.s, first.ID)
	if got.MessageID != "om_replacement" || !got.Ix.CreatedAt.Equal(replacement.CreatedAt) {
		t.Fatalf("an earlier terminal event damaged the replacement ledger: %+v", got)
	}
}

func TestFeishuStaleCallbackCannotActOnReplacement(t *testing.T) {
	needsLarkLayer(t)
	for _, action := range []string{"resolve", "clarify"} {
		for _, swapAfterGet := range []bool{false, true} {
			name := action
			if swapAfterGet {
				name += "/swap_after_get"
			} else {
				name += "/visible_replacement"
			}
			t.Run(name, func(t *testing.T) {
				ft := newFleetTestNoStore(t)
				preparedTestConversation(ft)
				first := proto.Interaction{ID: "ix_feishu_stale_callback", MachineID: "mac", SessionID: "s1",
					Kind: proto.IxPermission, OutwardWrite: true, CanClarify: true, CreatedAt: time.Now().UTC()}
				firstDone := startPreparedInteraction(t, ft, first, approvalops.Operation{})
				ft.s.fleet.mu.Lock()
				p := ft.s.fleet.interactions[first.ID]
				ft.s.fleet.mu.Unlock()

				replacement := first
				replacement.CreatedAt = first.CreatedAt.Add(time.Second)
				var applies atomic.Int32
				replacementDone := make(chan preparedTestResult, 1)
				var replacementStarted atomic.Bool
				startReplacement := func() {
					if !replacementStarted.CompareAndSwap(false, true) {
						return
					}
					go func() {
						out, err := ft.s.waitPreparedCard(context.Background(), replacement, approvalops.Operation{Apply: func(context.Context, proto.Resolution) (string, error) {
							applies.Add(1)
							return "replacement applied", nil
						}})
						replacementDone <- preparedTestResult{out: out, err: err}
					}()
				}
				t.Cleanup(func() {
					if !replacementStarted.Load() {
						return
					}
					ft.s.fleet.mu.Lock()
					current := ft.s.fleet.interactions[first.ID]
					ft.s.fleet.mu.Unlock()
					if current != nil {
						ft.s.endPrepared(current, "cancelled", "test cleanup")
					}
				})
				currentIsReplacement := func() bool {
					ft.s.fleet.mu.Lock()
					defer ft.s.fleet.mu.Unlock()
					current := ft.s.fleet.interactions[first.ID]
					return current != nil && current.ix.CreatedAt.Equal(replacement.CreatedAt)
				}
				if !swapAfterGet {
					ft.s.endPrepared(p, "cancelled", "test replacement")
					select {
					case <-firstDone:
					case <-time.After(5 * time.Second):
						t.Fatal("the first occurrence did not finish")
					}
					startReplacement()
					waitFor(t, "the visible replacement", currentIsReplacement)
				}

				var ownerActions atomic.Int32
				var swapped atomic.Bool
				ft.f.answerAppBlocking(AppInteractions, assistantwire.InteractionsOwnerAction, func(json.RawMessage) (any, error) {
					ownerActions.Add(1)
					if swapAfterGet {
						ft.s.endPrepared(p, "cancelled", "test replacement after get")
						startReplacement()
						if ownerActionPoll(t, 5*time.Second, currentIsReplacement) {
							swapped.Store(true)
							// This is the replacement announcement's ledger write. It
							// must survive the stale press's later cleanup/restore.
							keepChatCard(ft.s, first.ID, &botCard{MessageID: "om_replacement", Ix: replacement})
						}
					}
					return map[string]any{}, nil
				})
				ledger := replacement
				messageID := "om_replacement"
				if swapAfterGet {
					ledger = first
					messageID = "om_first"
				}
				keepChatCard(ft.s, first.ID, &botCard{MessageID: messageID, Ix: ledger})
				var a botcard.CardAction
				a.Action.Value = map[string]any{"a": "ix", "id": first.ID, "created_at": first.CreatedAt.Format(time.RFC3339Nano)}
				if action == "resolve" {
					a.Action.Value["allow"] = "1"
				} else {
					a.Action.Value["clarify"] = "1"
					a.Action.FormValue = map[string]any{"question": "stale question"}
				}
				a.Context.ChatID, a.Context.MessageID = "oc_owner", "om_card"
				resp := ft.s.larkModule().Action(context.Background(), &a, "stale-owner-action")
				if swapAfterGet {
					waitFor(t, "the real replacement at the owner-action boundary", swapped.Load)
				}
				if resp != nil && resp.Toast != nil && resp.Toast.Type == "success" {
					t.Fatalf("stale %s succeeded on the replacement: %+v", action, resp)
				}
				wantOwnerActions := int32(0)
				if swapAfterGet {
					// The old occurrence's action can be accepted before the
					// replacement; the final occurrence claim must still reject it.
					wantOwnerActions = 1
					select {
					case <-firstDone:
					case <-time.After(5 * time.Second):
						t.Fatal("the swapped first occurrence did not finish")
					}
				}
				if got := ownerActions.Load(); got != wantOwnerActions {
					t.Fatalf("stale %s consumed %d owner actions, want %d", action, got, wantOwnerActions)
				}
				if got := applies.Load(); got != 0 {
					t.Fatalf("stale resolve applied the replacement %d times", got)
				}
				if got := len(ownerQuestionSends(ft)); got != 0 {
					t.Fatalf("stale clarification sent %d questions to the replacement conversation", got)
				}
				gotLedger, _ := chatCard(ft.s, first.ID)
				if gotLedger.MessageID != "om_replacement" || !gotLedger.Ix.CreatedAt.Equal(replacement.CreatedAt) {
					t.Fatalf("stale %s damaged the replacement bot-card ledger: %+v", action, gotLedger)
				}
				ft.waitPending(1)
			})
		}
	}
}
