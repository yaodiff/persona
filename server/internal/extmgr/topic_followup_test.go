package extmgr

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/cliport"
	"github.com/nwverse/persona/server/internal/cliwire"
)

func TestLarkLinkedTopicReadUsesVerifiedHumanActivity(t *testing.T) {
	needsLarkCLI(t)
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name     string
		mutate   func(map[string]any)
		watch    string
		drift    bool
		disabled bool
		more     bool
		wantErr  bool
		want     int
	}{
		{name: "original requester", want: 1},
		{name: "another participant", mutate: func(m map[string]any) { m["sender"].(map[string]any)["id"] = "ou_another" }, want: 1},
		{name: "bootstrap configured identity", watch: "bootstrap", want: 1},
		{name: "wrong topic", mutate: func(m map[string]any) { m["thread_id"] = "omt_other" }},
		{name: "wrong chat", mutate: func(m map[string]any) { m["chat_id"] = "oc_other" }},
		{name: "wrong topic link", mutate: func(m map[string]any) {
			m["message_app_link"] = "https://applink.feishu.cn/client/thread/open?open_chat_id=oc_other&open_thread_id=omt_bound"
		}},
		{name: "no topic proof", mutate: func(m map[string]any) { delete(m, "thread_id"); m["root_id"] = "om_root" }},
		{name: "self", mutate: func(m map[string]any) { m["sender"].(map[string]any)["id"] = "ou_account" }},
		{name: "bot", mutate: func(m map[string]any) { m["sender"].(map[string]any)["sender_type"] = "app" }},
		{name: "human with app ID", mutate: func(m map[string]any) { m["sender"].(map[string]any)["app_id"] = "app_bound" }},
		{name: "forward", mutate: func(m map[string]any) { m["content"] = `{"merge_forward":{}}` }},
		{name: "deleted", mutate: func(m map[string]any) { m["deleted"] = true }},
		{name: "unsupported media", mutate: func(m map[string]any) { m["msg_type"] = "image" }},
		{name: "future", mutate: func(m map[string]any) { m["create_time"] = now.Add(time.Hour).Format(time.RFC3339) }, wantErr: true},
		{name: "unparseable time", mutate: func(m map[string]any) { m["create_time"] = "unknown" }, wantErr: true},
		{name: "wrong account", watch: "account", wantErr: true},
		{name: "wrong app", watch: "app", wantErr: true},
		{name: "wrong brand", watch: "brand", wantErr: true},
		{name: "identity drift during read", drift: true, wantErr: true},
		{name: "disabled reader", disabled: true, wantErr: true},
		{name: "repeated pagination", more: true, wantErr: true},
		{name: "old checkpoint bounds pagination", more: true, mutate: func(m map[string]any) {
			m["create_time"] = now.Add(-24 * time.Hour).Format(time.RFC3339)
		}, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]any{"message_id": "om_followup", "msg_type": "text", "create_time": now.Add(-time.Hour).Format(time.RFC3339),
				"sender":  map[string]any{"id": "ou_requester", "id_type": "open_id", "sender_type": "user"},
				"chat_id": "oc_bound", "thread_id": "omt_bound", "content": `{"text":"synthetic follow-up body"}`}
			if tc.mutate != nil {
				tc.mutate(m)
			}
			page := map[string]any{"messages": []any{m}, "has_more": tc.more, "page_token": "same_page"}
			bin := makeReplyFixtureCLI(t, page, tc.drift, "feishu")
			h := newTestManager(t)
			h.connectors = h.newConnectors(nil)
			if err := h.connectors.Add(cliport.NewConnector(cliwire.Config{Bin: bin})); err != nil {
				t.Fatal(err)
			}
			if tc.disabled {
				if err := h.connectors.SetEnabled(cliwire.ConnectorID, false); err != nil {
					t.Fatal(err)
				}
			}
			identity := map[string]any{"brand": "feishu", "app_id": "app_bound", "account_open_id": "ou_account"}
			switch tc.watch {
			case "bootstrap":
				identity = nil
			case "account":
				identity["account_open_id"] = "ou_other"
			case "app":
				identity["app_id"] = "app_other"
			case "brand":
				identity["brand"] = "lark"
			}
			params, _ := json.Marshal(map[string]any{"identity": identity, "chat_id": "oc_bound", "thread_id": "omt_bound",
				"since": now.Add(-3 * time.Hour).Format(time.RFC3339)})
			raw, handled, err := h.AppCall(context.Background(), assistantwire.AppCall{
				AppID: assistantwire.AppConnectors, Method: "connectors.lark_topic", Params: params})
			if !handled {
				t.Fatal("linked topic app call was not handled")
			}
			if tc.wantErr {
				if err == nil {
					t.Fatalf("unsafe source returned a receipt: %s", raw)
				}
				if tc.disabled {
					if _, err := os.Stat(filepath.Join(filepath.Dir(bin), "count")); !os.IsNotExist(err) {
						t.Fatal("disabled connector executed its CLI")
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Watch struct {
					Identity map[string]string `json:"identity"`
				} `json:"watch"`
				Messages []map[string]any `json:"messages"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Messages) != tc.want || got.Watch.Identity["account_open_id"] != "ou_account" ||
				strings.Contains(string(raw), "synthetic follow-up body") {
				t.Fatalf("wrong source-only receipt: %s", raw)
			}
		})
	}
}
