package i18n

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
)

func TestEnglishOriginInboxErrors(t *testing.T) {
	err := ErrEnglish("inbox-errors.no_item")
	if err.Error() != "no such inbox item" || Localize(err, "") != err.Error() {
		t.Fatal("legacy English error changed")
	}
	for _, l := range []Lang{En, ZhHans, Ja} {
		want := T(l, "inbox-errors.no_item")
		if got := Localize(err, l); got != want {
			t.Errorf("%s typed error: %q, want %q", l, got, want)
		}
		if got := Localize(errors.New(err.Error()), l); got != want {
			t.Errorf("%s RPC-flattened error: %q, want %q", l, got, want)
		}
	}
}

func TestInboxTextCatalogRoundTrip(t *testing.T) {
	for _, from := range []Lang{En, ZhHans, Ja} {
		for key := range catalogs()[from] {
			if !strings.HasPrefix(key, "inbox-errors.") {
				continue
			}
			args := Args{"actual": 2, "expected": 3, "target": `"xx"`, "max": 40, "repo": `"example"`}
			for _, to := range []Lang{En, ZhHans, Ja} {
				if got, want := InboxText(to, T(from, key, args)), T(to, key, args); got != want {
					t.Errorf("%s -> %s %s: %q, want %q", from, to, key, got, want)
				}
			}
		}
	}
}

// Every chain template round-trips between all three languages. Only a triage's own tier and its rule-based reasons
// are labels; another placeholder whose value happens to be a tier word (a sender, title, wait target, and so on) is data.
func TestInboxTextChainCatalogRoundTrip(t *testing.T) {
	const (
		triaged       = "assistant-inbox_chain.triaged"
		triagedUrgent = "assistant-inbox_chain.triaged_urgent"
		detail        = "assistant-inbox_chain.triaged_detail"
	)
	keySet := map[string]bool{}
	for _, form := range inboxTemplates().forms {
		if strings.HasPrefix(form.key, "assistant-inbox_chain.") {
			keySet[form.key] = true
		}
	}
	keys := make([]string, 0, len(keySet))
	for key := range keySet {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, from := range Langs {
		for _, to := range Langs {
			for _, key := range keys {
				args := Args{}
				for _, form := range inboxTemplates().forms {
					if form.key != key {
						continue
					}
					for _, name := range form.names {
						args[name] = "已折叠" // i18n-ignore: deliberately a known label in an opaque slot
					}
				}
				args["count"], args["n"] = 2, 2
				args["tier"] = T(from, "assistant-inbox_chain.tier_noise")
				args["reason"] = T(from, "assistant-inbox.by_rules")
				wantArgs := Args{}
				for name, value := range args {
					wantArgs[name] = value
				}
				if key == triaged || key == triagedUrgent || key == detail {
					wantArgs["tier"] = T(to, "assistant-inbox_chain.tier_noise")
				}
				if key == detail {
					wantArgs["reason"] = T(to, "assistant-inbox.by_rules")
				}
				if got, want := InboxText(to, T(from, key, args)), T(to, key, wantArgs); got != want {
					t.Errorf("%s -> %s %s: %q, want %q", from, to, key, got, want)
				}
			}
		}
	}
}

func TestInboxTextStoredSentReplyRoundTrip(t *testing.T) {
	// Stored chains carry these rendered strings, not the old sent_other key. Renaming that key must not strand them.
	stored := map[Lang]string{
		En:     "Sent a reply",
		ZhHans: "发了回复", // i18n-ignore: stored text predates the sent_reply key
		Ja:     "返信を送信",
	}
	for _, from := range Langs {
		for _, to := range Langs {
			if got, want := InboxText(to, stored[from]), stored[to]; got != want {
				t.Errorf("%s -> %s stored sent reply: %q, want %q", from, to, got, want)
			}
		}
	}
}

func TestInboxTextTriagedTranslatesPolicyReason(t *testing.T) {
	const detail = "assistant-inbox_chain.triaged_detail"
	for _, from := range Langs {
		for _, to := range Langs {
			for _, name := range []string{"Release notices", "已折叠"} { // i18n-ignore: a rule name is opaque even when it is a label
				reason := T(from, "assistant-inbox.by_policy", Args{"name": name})
				args := Args{"tier": T(from, "assistant-inbox_chain.tier_action"), "reason": reason}
				wantArgs := Args{"tier": T(to, "assistant-inbox_chain.tier_action"),
					"reason": T(to, "assistant-inbox.by_policy", Args{"name": name})}
				if got, want := InboxText(to, T(from, detail, args)), T(to, detail, wantArgs); got != want {
					t.Errorf("%s -> %s policy reason %q: %q, want %q", from, to, name, got, want)
				}
			}
		}
	}
}

func TestInboxTextTriagedKeepsUnknownAndModelWords(t *testing.T) {
	const detail = "assistant-inbox_chain.triaged_detail"
	for _, from := range Langs {
		for _, to := range Langs {
			for _, reason := range []string{"A model-written reason", "需要你", "模型觉得这不重要"} { // i18n-ignore: opaque model/user text
				args := Args{"tier": T(from, "assistant-inbox_chain.tier_fyi"), "reason": reason}
				wantArgs := Args{"tier": T(to, "assistant-inbox_chain.tier_fyi"), "reason": reason}
				if got, want := InboxText(to, T(from, detail, args)), T(to, detail, wantArgs); got != want {
					t.Errorf("%s -> %s model reason %q: %q, want %q", from, to, reason, got, want)
				}
			}
			args := Args{"tier": "An unknown tier"}
			if got, want := InboxText(to, T(from, "assistant-inbox_chain.triaged", args)), T(to, "assistant-inbox_chain.triaged", args); got != want {
				t.Errorf("%s -> %s unknown tier: %q, want %q", from, to, got, want)
			}
		}
	}
}

// The public response rewrites both halves of a stored triage, including an urgent one, without changing a model's
// reason or the existing failed-entry detail handling.
func TestInboxJSONLocalizesTriagedChain(t *testing.T) {
	chain := []any{
		map[string]any{"kind": "triaged", "text": T(ZhHans, "assistant-inbox_chain.triaged", Args{"tier": T(ZhHans, "assistant-inbox_chain.tier_noise")}),
			"detail": T(ZhHans, "assistant-inbox_chain.triaged_detail", Args{"tier": T(ZhHans, "assistant-inbox_chain.tier_noise"), "reason": T(ZhHans, "assistant-inbox.by_rules")})},
		map[string]any{"kind": "triaged", "text": T(ZhHans, "assistant-inbox_chain.triaged_urgent", Args{"tier": T(ZhHans, "assistant-inbox_chain.tier_action")}),
			"detail": T(ZhHans, "assistant-inbox_chain.triaged_detail", Args{"tier": T(ZhHans, "assistant-inbox_chain.tier_action"), "reason": "模型判断"})}, // i18n-ignore: opaque model reason
		map[string]any{"kind": "failed", "text": T(ZhHans, "assistant-inbox_chain.failed"), "detail": T(ZhHans, "assistant-inbox_chain.failed_timeout")},
	}
	raw, err := json.Marshal(map[string]any{"items": []any{map[string]any{
		"id": "example", "source": "mail", "kind": "mail", "triaged_by": "rules", "chain": chain,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range Langs {
		var out struct {
			Items []struct {
				Chain []struct {
					Text   string `json:"text"`
					Detail string `json:"detail"`
				} `json:"chain"`
			} `json:"items"`
		}
		if err := json.Unmarshal(InboxJSON(to, raw), &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Items) != 1 || len(out.Items[0].Chain) != 3 {
			t.Fatalf("%s: wrong response shape: %+v", to, out)
		}
		got := out.Items[0].Chain
		want := []struct{ text, detail string }{
			{T(to, "assistant-inbox_chain.triaged", Args{"tier": T(to, "assistant-inbox_chain.tier_noise")}),
				T(to, "assistant-inbox_chain.triaged_detail", Args{"tier": T(to, "assistant-inbox_chain.tier_noise"), "reason": T(to, "assistant-inbox.by_rules")})},
			{T(to, "assistant-inbox_chain.triaged_urgent", Args{"tier": T(to, "assistant-inbox_chain.tier_action")}),
				T(to, "assistant-inbox_chain.triaged_detail", Args{"tier": T(to, "assistant-inbox_chain.tier_action"), "reason": "模型判断"})}, // i18n-ignore: opaque model reason
			{T(to, "assistant-inbox_chain.failed"), T(to, "assistant-inbox_chain.failed_timeout")},
		}
		for i := range want {
			if got[i].Text != want[i].text || got[i].Detail != want[i].detail {
				t.Errorf("%s chain %d: got %q / %q, want %q / %q", to, i, got[i].Text, got[i].Detail, want[i].text, want[i].detail)
			}
		}
	}
}

func TestInboxJSONKeepsStoredDataAndUserContent(t *testing.T) {
	raw := []byte(`{"items":[{"id":"example","source":"mail","kind":"mail","title":"no such inbox item","preview":"no such inbox item","reason":"no such inbox item","triaged_by":"model","context":"no such inbox item","metadata":{"error":"no such inbox item"},"agent":{"error":"no such inbox item"},"handling":{"error":"the inbox item could not be saved"},"chain":[{"kind":"failed","text":"处理失败","detail":"no such inbox item"}],"mail":{"uid":9007199254740993}}],"triage":{"reason":"no such inbox item"}}`)
	for _, l := range []Lang{ZhHans, Ja} {
		out := InboxJSON(l, raw)
		var v struct {
			Items []struct {
				Title, Preview, Reason, Context string
				Metadata                        map[string]string
				Agent                           map[string]string
				Handling                        map[string]string
			}
		}
		if err := json.Unmarshal(out, &v); err != nil {
			t.Fatal(err)
		}
		it := v.Items[0]
		if it.Agent["error"] != T(l, "inbox-errors.no_item") || it.Handling["error"] != T(l, "inbox-errors.save_failed") {
			t.Fatalf("%s: response errors not localized: %s", l, out)
		}
		for _, s := range []string{it.Title, it.Preview, it.Reason, it.Context, it.Metadata["error"]} {
			if s != "no such inbox item" {
				t.Fatal("user/model/opaque text changed")
			}
		}
		if !strings.Contains(string(out), "9007199254740993") {
			t.Fatal("large opaque integer lost precision")
		}
	}
	if string(raw) != string(InboxJSON("", raw)) {
		t.Fatal("legacy response changed")
	}
	if !strings.Contains(string(raw), `"agent":{"error":"no such inbox item"}`) {
		t.Fatal("stored data mutated")
	}
	for _, opaque := range []string{`{"ok":true}`, `{"error":"third-party diagnostic"}`, `{"texts":["no such inbox item"]}`, `not JSON`, `{"error":"no such inbox item"} extra`} {
		if string(InboxJSON(Ja, []byte(opaque))) != opaque {
			t.Fatalf("opaque response changed: %s", opaque)
		}
	}
}

type inboxFieldError struct{}

func (inboxFieldError) Error() string      { return "field: no such inbox item" }
func (inboxFieldError) Text(l Lang) string { return "field: " + T(l, "inbox-errors.no_item") }

func TestTypedErrorAndNestedErrorArgs(t *testing.T) {
	field := inboxFieldError{}
	if Localize(field, Ja) != "field: "+T(Ja, "inbox-errors.no_item") {
		t.Fatal("typed field error not localized")
	}
	wrapped := fmt.Errorf("outer: %w", field)
	if Localize(wrapped, Ja) != wrapped.Error() {
		t.Fatal("wrapper context was discarded")
	}
	err := ErrEnglish("inbox-errors.translation_target", Args{"target": ErrEnglish("inbox-errors.no_item")})
	if Localize(err, Ja) != T(Ja, "inbox-errors.translation_target", Args{"target": T(Ja, "inbox-errors.no_item")}) {
		t.Fatal("nested error not localized")
	}
}

// The hub localizes an inbox list item by item (hub/inbox_forms.go): each item alone reads as it does within the whole
// answer, InboxJSON's if it changes, else InboxReencoded's once anything in the answer changed; so does the rest of the
// answer. Not when the answer is itself an item: then InboxJSON visits none of its list.
func TestInboxJSONItemByItem(t *testing.T) {
	zh := func(key string) string { b, _ := json.Marshal(T(ZhHans, key)); return string(b) }
	raw := []byte(`{"items":[` +
		`{"id":"a","source":"mail","kind":"mail","agent":{"error":` + zh("inbox-errors.no_item") + `},"mail":{"uid":9007199254740993}},` +
		`{"id":"b","source":"mail","kind":"mail","title":"<b>no such inbox item</b>","z":1.50,"a":[]},` +
		`{"error":` + zh("inbox-errors.no_item") + `},3],` +
		`"triage":{"reason":` + zh("assistant-inbox_triage.busy") + `},"as_of":"t"}`)
	for _, l := range []Lang{En, Ja} {
		var whole struct {
			Items  []json.RawMessage          `json:"items"`
			Triage json.RawMessage            `json:"triage"`
			Rest   map[string]json.RawMessage `json:"-"`
		}
		out := InboxJSON(l, raw)
		if err := json.Unmarshal(out, &whole); err != nil || len(whole.Items) != 4 {
			t.Fatalf("%s: %v %s", l, err, out)
		}
		var items []json.RawMessage
		var top map[string]json.RawMessage
		_ = json.Unmarshal(raw, &top)
		_ = json.Unmarshal(top["items"], &items)
		changed := 0
		for i, it := range items {
			form := InboxJSON(l, it)
			if string(form) != string(it) {
				changed++
			} else {
				form = InboxReencoded(it)
			}
			if string(form) != string(whole.Items[i]) {
				t.Errorf("%s item %d: %s, within the answer %s", l, i, form, whole.Items[i])
			}
		}
		delete(top, "items")
		rest, _ := json.Marshal(top)
		var restForm map[string]json.RawMessage
		_ = json.Unmarshal(InboxJSON(l, rest), &restForm)
		if string(restForm["triage"]) != string(whole.Triage) || changed != 2 {
			t.Errorf("%s: triage %s, within the answer %s; %d items changed, want 2", l, restForm["triage"], whole.Triage, changed)
		}
	}
	if !InboxIsItem([]byte(`{"id":"a","source":"mail","kind":"mail"}`)) || InboxIsItem([]byte(`{"id":"","source":"mail","kind":"mail"}`)) || InboxIsItem(raw) || InboxIsItem([]byte(`null`)) {
		t.Fatal("InboxIsItem")
	}
	if string(InboxReencoded([]byte(`{"b":"<","a":9007199254740993}`))) != `{"a":9007199254740993,"b":"\u003c"}` || string(InboxReencoded([]byte(`{`))) != `{` {
		t.Fatal("InboxReencoded")
	}
}
