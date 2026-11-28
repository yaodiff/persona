package lark

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
)

// A /read's answer held for the owner (read_held.go, #14437), on the fake core: where the turn's answer goes, and
// what a press of its buttons posts. The whole pipeline, with the hub's checks and a store, is the assistant's
// feishu_read_held_test.go.

// heldPosts splits the fake hub's posts into the private chat's and the group's (edits left out).
func heldPosts(posts []map[string]any) (dm, group []map[string]any) {
	for _, p := range posts {
		switch {
		case p["update"] == true:
		case p["chat_id"] == testChat:
			group = append(group, p)
		default:
			dm = append(dm, p)
		}
	}
	return dm, group
}

// A /read's answer is held for the owner unless the run was asked with the switch to answer in the group directly:
// the private chat gets the card the group would get (no process, no web link) with the two buttons, under a key of
// its own, nothing goes into the group, and the request is marked DONE as before. With the switch the answer goes
// under the request in the group, as it did before.
func TestReadAnswerIsHeldForTheOwner(t *testing.T) {
	for _, direct := range []bool{false, true} {
		name := "held"
		if direct {
			name = "direct"
		}
		t.Run(name, func(t *testing.T) {
			s, posts, _ := cotHub(t, false)
			withWebBot(s.hub.(*fakeHub), "https://hub.example.com")
			r, _ := cotRun(s)
			s.setRunExt(r, readChatKey, testChat)
			if direct {
				s.setRunExt(r, readDirectKey, "1")
			}
			s.finishChannelRun(r, &channel.Result{Answer: "结论：同一次发布"})
			dm, group := heldPosts(*posts)
			reacts := s.hub.(*fakeHub).appParams(AppFeishu, FeishuReact)
			if len(reacts) != 1 || reacts[0]["message_id"] != "om_m1" || reacts[0]["emoji"] != "DONE" {
				t.Fatalf("reactions %v", reacts)
			}
			if direct {
				if len(dm) != 0 || len(group) != 1 || group[0]["reply_to"] != "om_m1" || group[0]["no_detour"] != true {
					t.Fatalf("private posts %v, group posts %v", dm, group)
				}
				return
			}
			if len(group) != 0 || len(dm) != 1 {
				t.Fatalf("private posts %v, group posts %v", dm, group)
			}
			key := readAnswerKey(r.ID, r.WaitFrom(), 0)
			card, _ := dm[0]["card"].(string)
			if dm[0]["reply_to"] != "" || dm[0]["uuid"] != readHeldPrivateKey(key, "card") || dm[0]["uuid"] == key {
				t.Fatalf("the private card: %v", dm[0])
			}
			if !strings.Contains(card, "同一次发布") || !strings.Contains(card, `"do":"post"`) || !strings.Contains(card, `"do":"skip"`) ||
				!strings.Contains(card, `"id":"`+key+`"`) || strings.Contains(card, "hub.example.com") || strings.Contains(card, "运行") {
				t.Fatalf("the private card lacks the answer or its buttons, or has the process or the web link: %s", card)
			}
		})
	}
}

// A held /read that works past read_working_after tells the owner once, in the private chat, that it is on it (#16808):
// nothing before that, nothing in the group, nothing more on later ticks, no buttons; and nothing at all for a /read
// answered straight in the group, whose reaction is where its owner looks.
func TestReadWorkingNoteGoesOncePrivately(t *testing.T) {
	for _, direct := range []bool{false, true} {
		name := "held"
		if direct {
			name = "direct"
		}
		t.Run(name, func(t *testing.T) {
			s, posts, _ := cotHub(t, false)
			r, _ := cotRun(s)
			s.setRunExt(r, readChatKey, testChat)
			s.setRunExt(r, readAskKey, "分析问答报错原因")
			if direct {
				s.setRunExt(r, readDirectKey, "1")
			}
			ctx := context.Background()
			s.sink().progress(ctx, r, readWorkingAfter-time.Second, nil, "")
			if len(*posts) != 0 {
				t.Fatalf("a note before read_working_after: %v", *posts)
			}
			for range 3 {
				s.sink().progress(ctx, r, readWorkingAfter+time.Second, []string{"运行 `ls`"}, "")
			}
			dm, group := heldPosts(*posts)
			if len(group) != 0 {
				t.Fatalf("the group got %v", group)
			}
			if direct {
				if len(dm) != 0 {
					t.Fatalf("a direct /read told the owner privately: %v", dm)
				}
				return
			}
			if len(dm) != 1 {
				t.Fatalf("private notes %v, want one", dm)
			}
			card, _ := dm[0]["card"].(string)
			if dm[0]["uuid"] != readHeldPrivateKey("om_m1", "working") || dm[0]["reply_to"] != "" {
				t.Fatalf("the note: %v", dm[0])
			}
			if !strings.Contains(card, "分析问答报错原因") || !strings.Contains(card, "正在读群") || strings.Contains(card, `"do":"post"`) ||
				strings.Contains(card, "运行") {
				t.Fatalf("the note lacks the ask or the words, or has buttons or the process: %s", card)
			}
		})
	}
}

// A held /read whose card could not be sent because the hub was unreachable is tried again, under the same key; once
// it went, the run ends.
func TestReadAnswerHeldRetriesWhenUnreachable(t *testing.T) {
	s, _, _ := cotHub(t, false)
	r, _ := cotRun(s)
	s.setRunExt(r, readChatKey, testChat)
	var keys []string
	s.hub.(*fakeHub).answerApp(AppFeishu, FeishuPost, func(raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		key, _ := p["uuid"].(string)
		keys = append(keys, key)
		if len(keys) == 1 {
			return nil, errOffline
		}
		return map[string]any{"message_id": "om_dm"}, nil
	})
	s.finishChannelRun(r, &channel.Result{Answer: "结论"})
	if got, _ := s.channelRunByID(r.ID); got == nil {
		t.Fatal("the run ended although its answer did not get out")
	}
	if reacts := s.hub.(*fakeHub).appParams(AppFeishu, FeishuReact); len(reacts) != 0 {
		t.Fatalf("marked done before the answer was held: %v", reacts)
	}
	s.finishChannelRun(r, &channel.Result{Answer: "结论"})
	if got, _ := s.channelRunByID(r.ID); got != nil {
		t.Fatal("the run did not end once its answer was held")
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("keys %q", keys)
	}
}

// A recalled /read says nothing, held or not.
func TestReadAnswerHeldRecalledSaysNothing(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	r, _ := cotRun(s)
	s.setRunExt(r, readChatKey, testChat)
	s.finishChannelRun(r, &channel.Result{Quiet: true, End: channel.EndRecalled})
	if len(*posts) != 0 {
		t.Fatalf("posts %v", *posts)
	}
}

// Post to group sends the held card under the request, under the key the answer would have gone into the group by,
// once: a second press posts nothing more. Don't post never posts.
func TestReadAnswerHeldPress(t *testing.T) {
	for _, do := range []string{"post", "skip"} {
		t.Run(do, func(t *testing.T) {
			s, posts, _ := cotHub(t, false)
			s.hub.(*fakeHub).answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
				return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0),
					Features: FeishuFeatures{Main: true, Cards: true, ReadGroup: true}}, nil
			})
			r, _ := cotRun(s)
			s.setRunExt(r, readChatKey, testChat)
			key := readAnswerKey(r.ID, r.WaitFrom(), 0)
			s.finishChannelRun(r, &channel.Result{Answer: "结论：同一次发布"})
			press := func() *botcard.CardResponse {
				var a botcard.CardAction
				a.Action.Value = map[string]any{"a": "rd", "id": key, "do": do}
				a.Context.MessageID = "om_card"
				return s.chat().botCardAction(context.Background(), &a, "")
			}
			for i := 0; i < 2; i++ {
				if resp := press(); resp == nil || resp.Toast == nil {
					t.Fatalf("press %d was not answered: %+v", i+1, resp)
				}
			}
			_, group := heldPosts(*posts)
			if do == "skip" {
				if len(group) != 0 {
					t.Fatalf("Don't post posted: %v", group)
				}
				return
			}
			if len(group) != 1 || group[0]["reply_to"] != "om_m1" || group[0]["uuid"] != key || group[0]["no_detour"] != true {
				t.Fatalf("group posts %v", group)
			}
			if card, _ := group[0]["card"].(string); !strings.Contains(card, "同一次发布") || strings.Contains(card, `"a":"rd"`) {
				t.Fatalf("the group's card: %s", card)
			}
		})
	}
}
