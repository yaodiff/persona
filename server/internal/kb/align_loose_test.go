package kb

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A 153-character text and the same text with one more character: the live case of two renderings one character
// apart at the end.
var (
	looseLong153 = strings.Repeat("Synthetic status paragraph about the rollout plan. ", 3)[:153]
	looseLong154 = looseLong153 + "."
)

func TestLooseMatchRules(t *testing.T) {
	alerts := "Synthetic alert: latency above threshold on service checkout, host "
	hundred := strings.Repeat("abcdefghij", 10)
	cases := []struct {
		name, a, b string
		want       bool
	}{
		// 1. An image / media / sticker placeholder or one-sided Lark emoticon on one side only.
		{"image_trailing", "Please check the chart [image]", "Please check the chart", true},
		{"image_capitalized", "Please check the chart [Image]", "Please check the chart", true},
		{"photo_placeholder", "Please check the chart [photo]", "Please check the chart [image]", true},
		{"media_placeholder", "Please check the chart [media]", "Please check the chart", true},
		{"sticker_placeholder_en", "Please check the chart [sticker]", "Please check the chart", true},
		{"sticker_placeholder_cn", "Please check the chart [表情]", "Please check the chart", true},                     // i18n-ignore: archive data
		{"image_inline_cn", "[图片] Please check the chart", "Please check the chart", true},                            // i18n-ignore: archive data
		{"image_fullwidth_cn", "Please check the chart 【图片】", "Please check the chart", true},                         // i18n-ignore: archive data
		{"sticker_fullwidth_cn", "Please check the chart 【表情】", "Please check the chart", true},                       // i18n-ignore: archive data
		{"image_attachment_marker", "Please check the chart [附件](a.png)", "Please check the chart [image]", true},     // i18n-ignore: archive data
		{"onesided_feishu_emoji_cn", "Please review the rollout draft [捂脸]", "Please review the rollout draft", true}, // i18n-ignore: archive data
		{"onesided_feishu_emoji_colon", "Please review the rollout draft :THUMBSUP:", "Please review the rollout draft", true},
		{"cross_format_feishu_emoji", "Please review the rollout draft [微笑]", "Please review the rollout draft :SMILE:", true}, // i18n-ignore: archive data
		// 2. A leading @mention written another way or spaced differently.
		{"mention_spacing_cjk", "@示例 收到", "@示例收到", true}, // i18n-ignore: archive data
		{"mention_unresolved_key", "@_user_1 please review the draft", "@Example Peer please review the draft", true},
		{"mention_at_tag", `<at user_id="ou_x">Example</at> please review`, "@Example please review", true},
		{"mention_missing", "@Example Peer please review", "please review", true},
		// 3. A card or post: one side has only its title.
		{"card_title", "Weekly report", "Weekly report\nService A: healthy\nService B: degraded", true},
		{"card_title_prefixed", "[卡片] Weekly report", "Weekly report\nService A: healthy", true}, // i18n-ignore: archive data
		{"post_bold_title", "**Weekly report**", "Weekly report\nService A: healthy", true},
		// 4. Nearly identical long texts.
		{"one_trailing_char", looseLong153, looseLong154, true},
		{"one_substitution", hundred + "x" + hundred, hundred + "y" + hundred, true},

		// Not the same message.
		{"different_short", "Build passed", "Build failed", false},
		{"template_numbers", alerts + "1 of the pool", alerts + "2 of the pool", false},
		{"template_url", alerts + "https://example.com/Run", alerts + "https://example.com/run", false},
		{"short_similar", "Deploy started on staging now", "Deploy started on staging now!", false},
		{"three_percent", hundred + "xyz", hundred + "abc", false},
		{"title_too_short", "OK", "OK\nmore text follows here", false},
		{"title_not_first", "Service A: healthy", "Weekly report\nService A: healthy", false},
		{"mention_other_text", "@Alpha hello there", "@Beta goodbye now", false},
		{"mention_long_head", "@" + strings.Repeat("x", 50) + " please review", "please review", false},
		{"mention_short_tail", "@Alpha ok", "@Beta ok", false},
		{"not_a_mention", "Alpha please review", "Beta please review", false},
		{"placeholder_only", "[image]", "[图片]", false},                                                                     // i18n-ignore: archive data
		{"placeholder_only_fullwidth", "【图片】", "[photo]", false},                                                           // i18n-ignore: archive data
		{"different_feishu_emojis", "Please review the rollout draft [微笑]", "Please review the rollout draft [大哭]", false}, // i18n-ignore: archive data
		{"emoji_only", "[微笑]", "[大哭]", false},                                                                              // i18n-ignore: archive data
		{"non_placeholder_bracket_tag", "Deploy finished [failed]", "Deploy finished", false},
		{"non_placeholder_cn_tag", "构建完成 [失败]", "构建完成", false}, // i18n-ignore: archive data
		{"empty", "", "Weekly report", false},
		{"file_names", "[file alpha.pdf]", "[file beta.pdf]", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, b := looseForm(c.a, 1), looseForm(c.b, 1)
			if got, _ := looseMatch(a, b); got != c.want {
				t.Fatalf("looseMatch(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
			}
			if got, _ := looseMatch(b, a); got != c.want {
				t.Fatalf("looseMatch is not symmetric for %q, %q", c.a, c.b)
			}
		})
	}
}

func TestEditsWithin(t *testing.T) {
	for _, c := range []struct {
		a, b string
		k    int
		want bool
	}{
		{"kitten", "sitting", 3, true},
		{"kitten", "sitting", 2, false},
		{"", "ab", 2, true},
		{"abc", "abc", 0, true},
		{"abcdef", "abcxef", 1, true},
		{"abcdef", "ab", 3, false},
	} {
		if got := editsWithin([]rune(c.a), []rune(c.b), c.k); got != c.want {
			t.Errorf("editsWithin(%q, %q, %d) = %v, want %v", c.a, c.b, c.k, got, c.want)
		}
	}
}

// The four kinds of difference left after repair version 3, in live alignment and in repair version 4: the fuller copy
// stays once, in whichever archive had it; every receipt ID stays.
func TestGatewayAlignerLooseTierFourKinds(t *testing.T) {
	type side struct {
		text  string
		media []string
	}
	cases := []struct {
		name      string
		token     string // in both copies' first line, once
		gateway   side
		synced    side
		keepSync  bool // the sync copy is fuller: it stays and the gateway line goes
		syncLines int  // lines the sync copy takes in its day file
	}{
		// A trailing placeholder backed by an attachment was already the same message to the fingerprint tier.
		{"image_trailing_on_sync", "pine", side{"Please check the pine chart", nil}, side{"Please check the pine chart [image]", []string{"sy-img"}}, false, 1},
		{"image_leading_on_sync", "kiwi", side{"Please check the kiwi chart", nil}, side{"[image] Please check the kiwi chart", []string{"sy-img"}}, true, 1},
		{"image_on_gateway_only", "plum", side{"[图片] Please check the plum chart", []string{"gw-img"}}, side{"Please check the plum chart", nil}, false, 1}, // i18n-ignore: archive data
		{"mention_written_otherwise", "pear", side{"@Example Peer please review the pear draft", nil}, side{"@_user_1 please review the pear draft", nil}, false, 1},
		{"mention_spacing", "fig", side{"@示例 收到 fig", nil}, side{"@示例收到 fig", nil}, false, 1},                                                                           // i18n-ignore: archive data
		{"card_title_only_on_gateway", "lime", side{"[卡片] Weekly lime report", nil}, side{"Weekly lime report\nService A: healthy\nService B: degraded", nil}, true, 3}, // i18n-ignore: archive data
		{"one_char_shorter_on_sync", "yuzu", side{"yuzu " + looseLong154, nil}, side{"yuzu " + looseLong153, nil}, false, 1},
		{"one_char_longer_on_sync", "date", side{"date " + looseLong153, nil}, side{"date " + looseLong154, nil}, true, 1},
	}
	for _, archived := range []bool{false, true} {
		t.Run(fmt.Sprint("archived=", archived), func(t *testing.T) {
			e := testEngine(t, nil)
			ctx := context.Background()
			at := alignAt()
			gw := SourceChat{ID: "f_loose", Name: "Example loose group", Kind: "group"}
			sc := SourceChat{ID: "oc_loose", Name: gw.Name, Kind: "group"}
			if _, err := e.Ingest(ctx, named("larksync"), sc, []SourceMessage{
				{ID: "om_keep", SenderName: "Example Sender", Text: "Unique sync message", At: at.Add(-time.Minute)},
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Ingest(ctx, named("lark"), gw, []SourceMessage{
				{ID: "g_keep", SenderName: "Example Sender", Text: "Unique gateway message", At: at.Add(-time.Minute)},
			}); err != nil {
				t.Fatal(err)
			}
			var synced []SourceMessage
			for i, c := range cases {
				g := SourceMessage{ID: "gw_" + c.name, SenderName: "Example Sender", Text: c.gateway.text, Media: c.gateway.media, At: at.Add(time.Duration(i) * time.Minute)}
				if _, err := e.Ingest(ctx, named("lark"), gw, []SourceMessage{g}); err != nil {
					t.Fatal(err)
				}
				synced = append(synced, SourceMessage{ID: "sy_" + c.name, SenderName: g.SenderName, Text: c.synced.text, Media: c.synced.media, At: g.At.Add(time.Second)})
			}
			if !archived {
				e.Align = NewGatewayAligner(e.Store)
			}
			n, err := e.Ingest(ctx, named("larksync"), sc, synced)
			if err != nil {
				t.Fatal(err)
			}
			wantWritten := 0
			for _, c := range cases {
				if c.keepSync {
					wantWritten++
				}
			}
			if archived {
				if n != len(cases) {
					t.Fatalf("seed archived sync messages = %d, want %d", n, len(cases))
				}
				// A site that completed repair version 3 runs version 4.
				marker := filepath.Join(e.Store.Dir, KindChat, larkSyncRepairFile)
				if err := os.WriteFile(marker, []byte(`{"version":3}`), 0o600); err != nil {
					t.Fatal(err)
				}
				if dropped := e.DedupeLarkSyncChats(); dropped != len(cases) {
					t.Fatalf("repair v4 dropped %d lines, want %d (one copy of each)", dropped, len(cases))
				}
				if again := e.DedupeLarkSyncChats(); again != 0 {
					t.Fatalf("repair v4 rerun dropped %d, want 0", again)
				}
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
				if again := e.DedupeLarkSyncChats(); again != 0 {
					t.Fatalf("repair v4 without its marker dropped %d more, want 0 (idempotent)", again)
				}
			} else if n != wantWritten {
				t.Fatalf("live alignment wrote %d sync messages, want %d (the fuller ones)", n, wantWritten)
			}
			syncText := e.Store.Since("larksync-oc_loose", time.Time{}, 1<<20)
			gwText := e.Store.Since("lark-f_loose", time.Time{}, 1<<20)
			if !strings.Contains(syncText, "Unique sync message") || !strings.Contains(gwText, "Unique gateway message") {
				t.Fatalf("a message without a counterpart was lost:\nsync:\n%s\ngateway:\n%s", syncText, gwText)
			}
			syncMessages, gwMessages := 1, 1
			for _, c := range cases {
				inSync, inGw := strings.Count(syncText, c.token), strings.Count(gwText, c.token)
				if c.keepSync {
					syncMessages++
					if inSync != 1 || inGw != 0 {
						t.Errorf("%s: sync copy is fuller; got %d in sync, %d in gateway, want 1 and 0", c.name, inSync, inGw)
					}
				} else {
					gwMessages++
					if inSync != 0 || inGw != 1 {
						t.Errorf("%s: gateway copy stays; got %d in sync, %d in gateway, want 0 and 1", c.name, inSync, inGw)
					}
				}
			}
			if t.Failed() {
				t.Logf("sync:\n%s\ngateway:\n%s", syncText, gwText)
			}
			if c, _ := e.Store.Chat("larksync-oc_loose"); c.Messages != syncMessages {
				t.Errorf("sync chat Messages = %d, want %d", c.Messages, syncMessages)
			}
			if c, _ := e.Store.Chat("lark-f_loose"); c.Messages != gwMessages {
				t.Errorf("gateway chat Messages = %d, want %d", c.Messages, gwMessages)
			}
			for _, c := range cases {
				if _, ok := receiptIDs(t, e.Store, "larksync-oc_loose")["sy_"+c.name]; !ok {
					t.Errorf("sync receipt lost for %s", c.name)
				}
				if _, ok := receiptIDs(t, e.Store, "lark-f_loose")["gw_"+c.name]; !ok {
					t.Errorf("gateway receipt lost for %s", c.name)
				}
			}
			if readCrosswalk(t, e.Store)["larksync-oc_loose"] != "lark-f_loose" {
				t.Fatal("the loose match did not pair the chats")
			}

			// A gateway line dropped for a fuller sync copy keeps its receipt: the gateway delivering it again
			// writes nothing.
			var again []SourceMessage
			for i, c := range cases {
				again = append(again, SourceMessage{ID: "gw_" + c.name, SenderName: "Example Sender", Text: c.gateway.text, Media: c.gateway.media, At: at.Add(time.Duration(i) * time.Minute)})
			}
			if n, err := e.Ingest(ctx, named("lark"), gw, again); err != nil || n != 0 {
				t.Fatalf("gateway replay wrote %d, %v", n, err)
			}
		})
	}
}

// The loose tier refuses anything outside its frame: another sender, an unknown sender on an unpaired chat, another
// time, a template message whose numbers differ, two candidates, and a fuller sync copy whose gateway line cannot be
// told from an identical one.
func TestGatewayAlignerLooseTierRefusesDifferentMessages(t *testing.T) {
	alert := "Synthetic alert: latency above threshold on service checkout, host 1 of the pool"
	for _, name := range []string{"different_sender", "unknown_sender_unpaired", "different_time", "template_numbers", "ambiguous_lines", "identical_gateway_lines", "selection_not_written"} {
		t.Run(name, func(t *testing.T) {
			e, _ := alignEngine(t)
			ctx := context.Background()
			at := alignAt()
			g := SourceMessage{ID: "g1", SenderName: "Example Sender", Text: "Weekly report", At: at}
			m := SourceMessage{ID: "om_one", SenderName: g.SenderName, Text: "Weekly report\nService A: healthy", At: at.Add(time.Second)}
			switch name {
			case "different_sender":
				m.SenderName = "Another Sender"
			case "unknown_sender_unpaired":
				m.SenderName = ""
			case "different_time":
				m.At = at.Add(10 * time.Second)
			case "template_numbers":
				g.Text, m.Text = alert, strings.Replace(alert, "host 1", "host 2", 1)
			}
			gw := SourceChat{ID: "f_loose_safe", Name: "Example loose safe", Kind: "group"}
			gws := []SourceMessage{g}
			switch name {
			case "ambiguous_lines":
				gws = append(gws, SourceMessage{ID: "g2", SenderName: g.SenderName, Text: "[卡片] Weekly report", At: at}) // i18n-ignore: archive data
			case "identical_gateway_lines":
				gws = append(gws, SourceMessage{ID: "", SenderName: g.SenderName, Text: g.Text, At: at})
			}
			if _, err := e.Ingest(ctx, named("lark"), gw, gws); err != nil {
				t.Fatal(err)
			}
			before := e.Store.Since("lark-f_loose_safe", time.Time{}, 1<<20)
			if name == "selection_not_written" {
				// A sync message the aligner lets through but the archive does not write leaves the gateway's copy.
				e.Store.superseded = map[string]map[string]gatewayDrop{"larksync-oc_loose_safe": {"om_one": {chat: "lark-f_loose_safe", day: at.In(e.Store.Zone()).Format("2006-01-02"), key: "x"}}}
				e.settleSuperseded("larksync-oc_loose_safe")
				if after := e.Store.Since("lark-f_loose_safe", time.Time{}, 1<<20); after != before {
					t.Fatalf("a gateway line went for a sync message never written:\n%s", after)
				}
				if len(e.Store.superseded) != 0 {
					t.Fatal("the pending drop was not forgotten")
				}
				return
			}
			if n, err := e.Ingest(ctx, named("larksync"), SourceChat{ID: "oc_loose_safe", Name: gw.Name, Kind: "group"}, []SourceMessage{m}); err != nil || n != 1 {
				t.Fatalf("a different message must be written: added=%d err=%v", n, err)
			}
			if after := e.Store.Since("lark-f_loose_safe", time.Time{}, 1<<20); after != before {
				t.Fatalf("the gateway archive changed:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			if _, err := os.Stat(filepath.Join(e.Store.Dir, KindChat, crosswalkFile)); !os.IsNotExist(err) {
				t.Fatal("a refused match created a crosswalk")
			}
		})
	}
}

// Legacy paired gateway chats archived before .receipts.json existed have minute-only timestamps (dst.exact is zero).
// Both format and loose tiers fall back to the minute-level window on already-paired crosswalk chats (L1), strip the
// expanded English/Chinese media/sticker placeholders and one-sided Lark emoticons, keep the fuller copy, preserve all
// sync receipt IDs, and re-run idempotently when upgrading from repair version 4 to version 5.
func TestGatewayAlignerPairedWithoutReceiptsAndRepairV5(t *testing.T) {
	type side struct {
		sender string
		text   string
		media  []string
	}
	cases := []struct {
		name     string
		token    string
		gateway  side
		synced   side
		offset   time.Duration // within the same minute as the gateway line
		keepSync bool
	}{
		{
			name:     "english_placeholder_photo_vs_image_attachment",
			token:    "cedar",
			gateway:  side{sender: "Example Member", text: "Please check the cedar diagram [photo]"},
			synced:   side{sender: "", text: "Please check the cedar diagram [image]", media: []string{"sy-img"}},
			offset:   25 * time.Second,
			keepSync: true,
		},
		{
			name:     "fullwidth_chinese_placeholder",
			token:    "birch",
			gateway:  side{sender: "Example Member", text: "Please review the birch diagram 【图片】"}, // i18n-ignore: archive data
			synced:   side{sender: "", text: "Please review the birch diagram"},
			offset:   30 * time.Second,
			keepSync: false,
		},
		{
			name:     "onesided_chinese_feishu_emoticon",
			token:    "maple",
			gateway:  side{sender: "Example Peer", text: "Reminder @Example Team check the maple release notes before lunch"},
			synced:   side{sender: "", text: "Reminder @Example Team check the maple release notes before lunch [捂脸]"}, // i18n-ignore: archive data
			offset:   35 * time.Second,
			keepSync: false,
		},
		{
			name:     "card_title_only_on_gateway",
			token:    "alder",
			gateway:  side{sender: "Example Bot", text: "Weekly alder summary"},
			synced:   side{sender: "", text: "Weekly alder summary\nService A: healthy\nService B: healthy"},
			offset:   40 * time.Second,
			keepSync: true,
		},
		{
			name:     "one_char_apart_same_sender",
			token:    "aspen",
			gateway:  side{sender: "Example Author", text: "aspen " + looseLong154},
			synced:   side{sender: "Example Author", text: "aspen " + looseLong153},
			offset:   45 * time.Second,
			keepSync: false,
		},
		{
			name:     "format_tier_link_without_receipts",
			token:    "hazel",
			gateway:  side{sender: "Example Peer", text: "Read [hazel guide](https://example.com/guide)"},
			synced:   side{sender: "", text: "Read hazel guide (https://example.com/guide)"},
			offset:   20 * time.Second,
			keepSync: false,
		},
	}

	for _, archived := range []bool{false, true} {
		t.Run(fmt.Sprint("archived=", archived), func(t *testing.T) {
			e := testEngine(t, nil)
			ctx := context.Background()
			// Start at :05 of a minute so +20s..+45s offsets all land inside the exact same minute.
			base := time.Now().Add(-time.Hour).Truncate(time.Minute).Add(5 * time.Second)
			gw := SourceChat{ID: "f_legacy", Name: "Example legacy group", Kind: "group"}
			sc := SourceChat{ID: "oc_legacy", Name: gw.Name, Kind: "group"}

			if _, err := e.Ingest(ctx, named("larksync"), sc, []SourceMessage{
				{ID: "om_keep", SenderName: "Example Sender", Text: "Unique sync message", At: base.Add(-2 * time.Minute)},
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Ingest(ctx, named("lark"), gw, []SourceMessage{
				{ID: "g_keep", SenderName: "Example Sender", Text: "Unique gateway message", At: base.Add(-2 * time.Minute)},
			}); err != nil {
				t.Fatal(err)
			}

			var synced []SourceMessage
			for i, c := range cases {
				at := base.Add(time.Duration(i*2) * time.Minute)
				g := SourceMessage{ID: "gw_" + c.name, SenderName: c.gateway.sender, Text: c.gateway.text, Media: c.gateway.media, At: at}
				if _, err := e.Ingest(ctx, named("lark"), gw, []SourceMessage{g}); err != nil {
					t.Fatal(err)
				}
				synced = append(synced, SourceMessage{ID: "sy_" + c.name, SenderName: c.synced.sender, Text: c.synced.text, Media: c.synced.media, At: at.Add(c.offset)})
			}

			// Remove the gateway chat's .receipts.json to simulate a legacy archive with minute-only lines,
			// and pre-populate the crosswalk pairing (L1).
			if err := os.Remove(e.Store.path(KindChat, "lark-f_legacy", ingestReceipts)); err != nil {
				t.Fatal(err)
			}
			walk, _ := json.Marshal(map[string]string{"larksync-oc_legacy": "lark-f_legacy"})
			if err := os.WriteFile(filepath.Join(e.Store.Dir, KindChat, crosswalkFile), walk, 0o600); err != nil {
				t.Fatal(err)
			}

			if !archived {
				e.Align = NewGatewayAligner(e.Store)
			}
			n, err := e.Ingest(ctx, named("larksync"), sc, synced)
			if err != nil {
				t.Fatal(err)
			}

			wantWritten := 0
			for _, c := range cases {
				if c.keepSync {
					wantWritten++
				}
			}
			if archived {
				if n != len(cases) {
					t.Fatalf("seed archived sync messages = %d, want %d", n, len(cases))
				}
				// A site that already ran repair version 4 triggers repair version 5.
				marker := filepath.Join(e.Store.Dir, KindChat, larkSyncRepairFile)
				if err := os.WriteFile(marker, []byte(`{"version":4}`), 0o600); err != nil {
					t.Fatal(err)
				}
				if dropped := e.DedupeLarkSyncChats(); dropped != len(cases) {
					t.Fatalf("repair v5 dropped %d lines, want %d", dropped, len(cases))
				}
				if larkSyncRepairVersion != 5 {
					t.Fatalf("larkSyncRepairVersion = %d, want 5", larkSyncRepairVersion)
				}
				var state larkSyncRepairState
				if ok, err := readJSON(marker, &state); err != nil || !ok || state.Version != larkSyncRepairVersion {
					t.Fatalf("repair marker = %+v (ok=%v err=%v), want version %d", state, ok, err, larkSyncRepairVersion)
				}
				if again := e.DedupeLarkSyncChats(); again != 0 {
					t.Fatalf("repair v5 rerun dropped %d, want 0", again)
				}
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
				if again := e.DedupeLarkSyncChats(); again != 0 {
					t.Fatalf("repair v5 rerun without marker dropped %d, want 0 (idempotent)", again)
				}
			} else if n != wantWritten {
				t.Fatalf("live alignment wrote %d sync messages, want %d", n, wantWritten)
			}

			syncText := e.Store.Since("larksync-oc_legacy", time.Time{}, 1<<20)
			gwText := e.Store.Since("lark-f_legacy", time.Time{}, 1<<20)
			if !strings.Contains(syncText, "Unique sync message") || !strings.Contains(gwText, "Unique gateway message") {
				t.Fatalf("unique line lost:\nsync:\n%s\ngateway:\n%s", syncText, gwText)
			}
			syncMessages, gwMessages := 1, 1
			for _, c := range cases {
				inSync, inGw := strings.Count(syncText, c.token), strings.Count(gwText, c.token)
				if c.keepSync {
					syncMessages++
					if inSync != 1 || inGw != 0 {
						t.Errorf("%s: fuller sync copy must stay; got %d in sync, %d in gateway", c.name, inSync, inGw)
					}
				} else {
					gwMessages++
					if inSync != 0 || inGw != 1 {
						t.Errorf("%s: gateway copy must stay; got %d in sync, %d in gateway", c.name, inSync, inGw)
					}
				}
			}
			if c, _ := e.Store.Chat("larksync-oc_legacy"); c.Messages != syncMessages {
				t.Errorf("sync chat Messages = %d, want %d", c.Messages, syncMessages)
			}
			if c, _ := e.Store.Chat("lark-f_legacy"); c.Messages != gwMessages {
				t.Errorf("gateway chat Messages = %d, want %d", c.Messages, gwMessages)
			}
			for _, c := range cases {
				if _, ok := receiptIDs(t, e.Store, "larksync-oc_legacy")["sy_"+c.name]; !ok {
					t.Errorf("sync receipt lost for %s", c.name)
				}
			}
		})
	}
}

// On a gateway chat without .receipts.json, different messages sent in the exact same minute are never merged — and
// unpaired chats never use minute-level fallback in the format or loose tiers.
func TestGatewayAlignerPairedWithoutReceiptsRefusesDifferentSameMinute(t *testing.T) {
	alert := "Synthetic alert: latency above threshold on service checkout, host 1 of the pool"
	cases := []struct {
		name   string
		paired bool
		gws    []SourceMessage
		sync   SourceMessage
	}{
		{
			name:   "different_short_text_same_minute",
			paired: true,
			gws:    []SourceMessage{{ID: "g1", SenderName: "Example Sender", Text: "Build passed"}},
			sync:   SourceMessage{ID: "om_1", SenderName: "Example Sender", Text: "Build failed"},
		},
		{
			name:   "template_different_number_same_minute",
			paired: true,
			gws:    []SourceMessage{{ID: "g1", SenderName: "Example Sender", Text: alert}},
			sync:   SourceMessage{ID: "om_1", SenderName: "Example Sender", Text: strings.Replace(alert, "host 1", "host 2", 1)},
		},
		{
			name:   "non_placeholder_bracket_tags_same_minute",
			paired: true,
			gws:    []SourceMessage{{ID: "g1", SenderName: "Example Sender", Text: "Deploy finished [staging]"}},
			sync:   SourceMessage{ID: "om_1", SenderName: "Example Sender", Text: "Deploy finished [prod]"},
		},
		{
			name:   "non_placeholder_cn_tag_same_minute",
			paired: true,
			gws:    []SourceMessage{{ID: "g1", SenderName: "Example Sender", Text: "构建完成 [失败]"}}, // i18n-ignore: archive data
			sync:   SourceMessage{ID: "om_1", SenderName: "Example Sender", Text: "构建完成"},        // i18n-ignore: archive data
		},
		{
			name:   "different_known_sender_same_minute",
			paired: true,
			gws:    []SourceMessage{{ID: "g1", SenderName: "Example Sender", Text: "Weekly report"}},
			sync:   SourceMessage{ID: "om_1", SenderName: "Another Sender", Text: "Weekly report\nService A: healthy"},
		},
		{
			name:   "unknown_sender_vs_owner_same_minute",
			paired: true,
			gws:    []SourceMessage{{ID: "g1", SenderName: "Owner", Mine: true, Text: "Weekly report"}},
			sync:   SourceMessage{ID: "om_1", SenderName: "", Text: "Weekly report\nService A: healthy"},
		},
		{
			name:   "ambiguous_two_gateway_candidates_same_minute",
			paired: true,
			gws: []SourceMessage{
				{ID: "g1", SenderName: "Example Sender", Text: "Weekly report"},
				{ID: "g2", SenderName: "Example Sender", Text: "[卡片] Weekly report"}, // i18n-ignore: archive data
			},
			sync: SourceMessage{ID: "om_1", SenderName: "Example Sender", Text: "Weekly report\nService A: healthy"},
		},
		{
			name:   "unnamed_attachment_only_same_minute",
			paired: true,
			gws:    []SourceMessage{{ID: "g1", SenderName: "Example Sender", Text: "[图片]", Media: []string{"gw-img"}}}, // i18n-ignore: archive data
			sync:   SourceMessage{ID: "om_1", SenderName: "", Text: "[image]", Media: []string{"sy-img"}},
		},
		{
			name:   "unpaired_without_receipts_refused",
			paired: false,
			gws:    []SourceMessage{{ID: "g1", SenderName: "Example Sender", Text: "Weekly report"}},
			sync:   SourceMessage{ID: "om_1", SenderName: "Example Sender", Text: "Weekly report\nService A: healthy"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, _ := alignEngine(t)
			ctx := context.Background()
			base := time.Now().Add(-time.Hour).Truncate(time.Minute).Add(5 * time.Second)
			gw := SourceChat{ID: "f_safe_norec", Name: "Example safe no receipts", Kind: "group"}
			sc := SourceChat{ID: "oc_safe_norec", Name: gw.Name, Kind: "group"}

			gws := make([]SourceMessage, len(c.gws))
			for i, g := range c.gws {
				g.At = base.Add(time.Duration(i) * 5 * time.Second)
				gws[i] = g
			}
			if _, err := e.Ingest(ctx, named("lark"), gw, gws); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(e.Store.path(KindChat, "lark-f_safe_norec", ingestReceipts)); err != nil {
				t.Fatal(err)
			}
			if c.paired {
				walk, _ := json.Marshal(map[string]string{"larksync-oc_safe_norec": "lark-f_safe_norec"})
				if err := os.WriteFile(filepath.Join(e.Store.Dir, KindChat, crosswalkFile), walk, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := e.Store.Since("lark-f_safe_norec", time.Time{}, 1<<20)

			m := c.sync
			m.At = base.Add(30 * time.Second) // exact same minute as gws
			if n, err := e.Ingest(ctx, named("larksync"), sc, []SourceMessage{m}); err != nil || n != 1 {
				t.Fatalf("different/uncertain message must be kept: added=%d err=%v", n, err)
			}
			if after := e.Store.Since("lark-f_safe_norec", time.Time{}, 1<<20); after != before {
				t.Fatalf("gateway archive changed:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			if !c.paired {
				if _, err := os.Stat(filepath.Join(e.Store.Dir, KindChat, crosswalkFile)); !os.IsNotExist(err) {
					t.Fatal("unpaired minute-only match created a crosswalk")
				}
			}
		})
	}
}
