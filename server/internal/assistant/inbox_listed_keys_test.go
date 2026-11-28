package assistant

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

// listedDrops are what listed() leaves out of an item with every exported field set (TestListedKeys), as JSON key
// paths ("agent.pending", "chain[].turn_at"; a map's one key is "x"), each with why. A path covers the paths under it.
// The list carries everything else the item encodes. The plan's step S8 moves InboxItem into a package of its own;
// this test and TestWireSamples are how that move shows the list did not change
// (docs/plans/plan-inbox-vertical-slice.md, section 3.2).
var listedDrops = map[string]string{
	// The item's bookkeeping, persisted, not listed (inbox.go: InboxItem).
	"context":                 "the conversation as text, for triage and Hand to AI; GET /api/v1/inbox/{id} returns it",
	"last":                    "bookkeeping: the newest message position or mail uid",
	"external_asked":          "bookkeeping: the source receipt that owns the to-do confirmation",
	"external_route_of":       "bookkeeping: the source version reserved for scoped routing",
	"triaged_at":              "bookkeeping: Last when it was triaged",
	"body_of":                 "bookkeeping: Last when a mail's full text was read",
	"judged_at":               "bookkeeping: Last when model 2 looked at it",
	"folder_of":               "bookkeeping: Last when the model was asked for its folder",
	"rules":                   "the item's own triage rules, from its connector",
	"triage_rev":              "bookkeeping: the tier criteria's revision it was sorted under",
	"rule_of":                 "bookkeeping: Last when the rules gave the tier",
	"settled_of":              "bookkeeping: the evidence it was filed on as handled elsewhere",
	"replied_of":              "bookkeeping: Last when a state report recorded the owner's reply",
	"arrived_of":              "bookkeeping: what ArrivedAt stamps",
	"arrived_at":              "bookkeeping: when it first held Last",
	"meta":                    "the connector's event meta, kept and handed back as it is",
	"group":                   "the event's conversation (inbox_group.go)",
	"chain_seq":               "bookkeeping: numbers the chain's entries",
	"suggest_seq":             "bookkeeping: the last suggestion's version",
	"reply_policy":            "bookkeeping: latest server-side reply-policy classification (inbox_reply_policy.go)",
	"reply_content_sources":   "#11901: internal member-text provenance for reply-policy scope after merge and Undo",
	"reply_context_truncated": "#11901: internal incomplete-classification-input marker, not client content",
	"mail_body_truncated":     "#11901: persisted mail-body clipping state, not client content",
	"reply_instruction":       "#11901: durable owner rewrite provenance, not another client-visible chain entry",
	"rejected_reply_draft":    "#11901: rejected on-demand exact-text provenance, not a client draft",
	"rejected_reply_drafts":   "#11901: every rejected on-demand exact text, not client drafts",
	"title_by":                "who wrote the title (inbox_follow.go): kept, not listed",
	// Kept so that Undo of a merge puts the title back (inbox_merge_title.go).
	"pre_merge_title":    "kept for Undo of a merge",
	"pre_merge_title_by": "kept for Undo of a merge",
	"merge_title":        "kept for Undo of a merge",
	// The current suggestion's own bookkeeping (inbox_handling.go: InboxSuggestion).
	"suggestion.cwd":                       "where an agent suggestion's Agent starts",
	"suggestion.mail_target":               "the reply's internal conflict check, not a client's target to choose",
	"suggestion.owner":                     "binds an unsent reply to its author",
	"suggestion.source_of":                 "binds an unsent reply to its source snapshot",
	"suggestion.reply_policy":              "binds an accepted reply suggestion to its server-side reply-policy snapshot (inbox_reply_policy.go)",
	"suggestion.on_demand":                 "#11901: distinguishes an on-demand generated draft from owner-authored text",
	"suggestion.reply_publication_pending": "#11901: internal two-phase durable-publication marker",
	"suggestion.reply_instruction":         "#11901: binds authorization to the exact instructed generated text",
	"suggestion.reply_publication_failed":  "#11901: fail-closed unpublished-model-text tombstone, not client content",
	// A send attempt's identity and frozen payload (inbox_suggestion_attempt.go: listedSuggestionAttempts).
	"suggestion_attempts[].client_id":       "the attempt's send identity",
	"suggestion_attempts[].target":          "the attempt's frozen target",
	"suggestion_attempts[].proposal":        "the attempt's frozen payload",
	"suggestion_attempts[].category":        "the attempt's reply-policy category snapshot",
	"suggestion_attempts[].route":           "the attempt's server-resolved reply-policy route",
	"suggestion_attempts[].content_version": "the attempt's item content version snapshot",
	"suggestion_attempts[].policy_revision": "the attempt's reply-policy revision snapshot",
	"suggestion_attempts[].reviewed":        "the attempt's independent-review authorization snapshot",
	"suggestion_attempts[].mentions_set":    "the attempt's send binding: its mentions were fixed at the send, not recomputed",
	// The processing conversation's bookkeeping (inbox_agent.go: InboxAgent).
	"agent.pending":                "notes waiting to be told to the processing conversation",
	"agent.wake":                   "whether those notes start a turn",
	"agent.sent_of":                "Last when Persona sent a confirmed reply",
	"agent.lost":                   "conversations found gone",
	"agent.processing_turn":        "the latest prompt issued to the processing conversation",
	"agent.interrupted_by_disable": "whether the off transition interrupted unfinished work, for one recovery pass",
	"agent.deferred_at":            "when a held wake joined the eligibility queue",
	"agent.wake_slot":              "the processing conversation's reserved slot",
	// Deterministic PR/Issue to-do auto-forward bookkeeping (inbox_forward.go: InboxTodoForward).
	"todo_forward.last": "bookkeeping: the item's Last when deterministic auto-forward last acted",
	// Each handoff's request bookkeeping (inbox_handling.go: InboxHandoff).
	"handoffs[].op":                 "the request's client id",
	"handoffs[].pending_suggestion": "coalesces Wrap up requests",
	"handoffs[].source_of":          "the source snapshot before the handoff send",
	"chain[].turn_at":               "node-side dedup bookkeeping (listedChain)",
	"connector.avatar":              "the source's own value: the apps load avatar_url instead",
	// The list shows a translation's subject and preview; the body is fetched when the mail opens (inbox_translate.go).
	"translations.x.text":              "the whole body",
	"translations.x.of":                "the item's Last it translates",
	"translations.x.model_fingerprint": "which model translated it",
}

// listedDropsOpenPRs are paths open PRs add to the item and leave out of the list: listed here so those PRs merge
// without touching this file. Move an entry to listedDrops when its PR lands; drop it when the PR closes.
var listedDropsOpenPRs = map[string]string{
	"visibility_content_refs":              "#11902: internal visibility provenance for merged and undone content, not client content",
	"tier_was":                             "#4512: what the item had before the user moved it, for Undo",
	"reply_approval":                       "#9790, #10580, #10587: the overdue reply reminder's receipt",
	"reply_content_sources":                "#11901: internal member-text provenance for reply-policy scope after merge and Undo",
	"reply_context_truncated":              "#11901: internal incomplete-classification-input marker, not client content",
	"reply_instruction":                    "#11901: durable owner rewrite provenance, not another client-visible chain entry",
	"rejected_reply_draft":                 "#11901: rejected on-demand exact-text provenance, not a client draft",
	"rejected_reply_drafts":                "#11901: every rejected on-demand exact text, not client drafts",
	"suggestion.on_demand":                 "#11901: distinguishes an on-demand generated draft from owner-authored text",
	"suggestion.reply_publication_pending": "#11901: internal two-phase durable-publication marker",
	"suggestion.reply_instruction":         "#11901: binds authorization to the exact instructed generated text",
	"suggestion.reply_publication_failed":  "#11901: fail-closed unpublished-model-text tombstone, not client content",
}

// listed() lists every key of a filled item but listedDrops, adds none, and computes the keys it owns (status, now,
// version, mutes, conversation, the connector's avatar_url). Two items: one open (in a section) and one done.
func TestListedKeys(t *testing.T) {
	open := filledInboxItem(t)
	open.DoneAt, open.MergedInto = nil, "" // open(): in a section, so listed() writes status
	done := filledInboxItem(t)
	done.DoneBy = "user" // a known filing, so listed() writes its now key
	for _, c := range []struct {
		name  string
		it    *InboxItem
		drops []string // past listedDrops
	}{
		{"open", open, nil},
		{"done", done, []string{"status"}}, // a done item has no section
	} {
		t.Run(c.name, func(t *testing.T) {
			all, listed := jsonPaths(t, c.it), jsonPaths(t, c.it.listed())
			dropped := func(p string) (string, bool) {
				for {
					if why, ok := listedDrops[p]; ok {
						return why, true
					}
					if why, ok := listedDropsOpenPRs[p]; ok {
						return why, true
					}
					if slices.Contains(c.drops, p) {
						return "this case's", true
					}
					i := strings.LastIndexAny(p, ".[")
					if i < 0 {
						return "", false
					}
					p = p[:i]
				}
			}
			for _, p := range all {
				why, drop := dropped(p)
				switch in := slices.Contains(listed, p); {
				case drop && in:
					t.Errorf("listed() lists %s, which listedDrops leaves out (%s)", p, why)
				case !drop && !in:
					t.Errorf("listed() leaves out %s: list it, or add it to listedDrops with why the list does not carry it", p)
				}
			}
			for _, p := range listed {
				if !slices.Contains(all, p) {
					t.Errorf("listed() adds %s, which the item does not have", p)
				}
			}
		})
	}
	// Every entry still names something: an item without the path no longer has it to leave out.
	all := jsonPaths(t, filledInboxItem(t))
	for p := range listedDrops {
		if !slices.Contains(all, p) {
			t.Errorf("listedDrops names %s, which InboxItem no longer has: drop the entry", p)
		}
	}
	for _, key := range []string{"status", "now", "version", "mutes", "conversation", "connector.avatar_url"} {
		if !slices.Contains(jsonPaths(t, open.listed()), key) {
			t.Errorf("listed() does not write %s for the open item", key)
		}
	}
}

func TestInboxVisibilityReview12ListedRedactsVisibilityContentRefs(t *testing.T) {
	refs := []InboxMergeRef{{ID: "synthetic-private-member", Source: "synthetic-private", Title: "SYNTHETIC-PRIVATE-TITLE"}}
	it := &InboxItem{ID: "synthetic-survivor", VisibilityContentRefs: refs}
	listed := it.listed()
	if listed.VisibilityContentRefs != nil {
		t.Errorf("listed() exposed visibility content refs: %+v", listed.VisibilityContentRefs)
	}
	if len(it.VisibilityContentRefs) != 1 || it.VisibilityContentRefs[0] != refs[0] {
		t.Errorf("listed() changed the stored item's visibility content refs: %+v", it.VisibilityContentRefs)
	}
}

// filledInboxItem is an item with every exported field set, at every depth (TestListedKeys), plus the few values
// listed() reads to decide what it writes.
func filledInboxItem(t *testing.T) *InboxItem {
	t.Helper()
	it := &InboxItem{}
	fillExported(t, reflect.ValueOf(it).Elem(), "InboxItem", nil)
	it.Connector.Tags[0].Key = connector.TagRepo // mutesOf offers only a repository, chat or sender
	if len(it.SuggestionAttempts) > 0 {
		// An attempt for another version than the current suggestion's: the agent's draft is listed (with a confirmed
		// attempt for it, listed() withholds the old clients' draft; inbox_suggestion_attempt_test.go has that case).
		it.SuggestionAttempts[0].Version = it.Suggestion.Version + 1
	}
	return it
}

// fillExported sets v and every exported field under it to a value that encodes: strings "x", numbers 1, true, a
// fixed time, one element in each list and map (key "x"). A struct type met again under itself stays zero.
func fillExported(t *testing.T, v reflect.Value, path string, outer []reflect.Type) {
	t.Helper()
	if v.Type() == reflect.TypeFor[time.Time]() {
		v.Set(reflect.ValueOf(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)))
		return
	}
	if v.Type() == reflect.TypeFor[json.RawMessage]() {
		v.SetBytes([]byte(`"x"`))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	case reflect.Pointer:
		if slices.Contains(outer, v.Type().Elem()) {
			return
		}
		v.Set(reflect.New(v.Type().Elem()))
		fillExported(t, v.Elem(), path, outer)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillExported(t, v.Index(0), path+"[]", outer)
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			t.Fatalf("%s: a map keyed by %s: teach fillExported its key", path, v.Type().Key())
		}
		e := reflect.New(v.Type().Elem()).Elem()
		fillExported(t, e, path+".x", outer)
		v.Set(reflect.MakeMap(v.Type()))
		v.SetMapIndex(reflect.ValueOf("x").Convert(v.Type().Key()), e)
	case reflect.Struct:
		outer = append(outer, v.Type())
		for i := range v.NumField() {
			if f := v.Type().Field(i); f.IsExported() {
				fillExported(t, v.Field(i), path+"."+f.Name, outer)
			}
		}
	case reflect.Interface:
		if !reflect.TypeFor[string]().Implements(v.Type()) {
			t.Fatalf("%s: an interface a string does not satisfy: teach fillExported a value for it", path)
		}
		v.Set(reflect.ValueOf("x"))
	default:
		t.Fatalf("%s: a field of kind %s: teach fillExported a value for it", path, v.Kind())
	}
}

// jsonPaths are the key paths v encodes to: "agent.draft", "chain[].kind", "translations.x.subject", sorted.
func jsonPaths(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, e := range v {
				p := k
				if prefix != "" {
					p = prefix + "." + k
				}
				paths[p] = true
				walk(p, e)
			}
		case []any:
			for _, e := range v {
				walk(prefix+"[]", e)
			}
		}
	}
	walk("", doc)
	return slices.Sorted(maps.Keys(paths))
}
