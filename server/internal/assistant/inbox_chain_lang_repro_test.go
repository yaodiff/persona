package assistant

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

// A chain entry is written in the user's language at write time; the hub's boundary localization (i18n.InboxJSON) turns
// it into the reader's language. The triaged entry's text holds the tier word inside it, so the boundary must translate
// that word too: an English reader of a row written in Chinese must see "Sorted: Folded", not "Sorted: 已折叠".
func TestInboxChainTriagedTextTranslatesItsTierWord(t *testing.T) {
	it := InboxItem{ID: "chain-lang", Source: "mail", Kind: KindMail, Tier: TierNoise,
		Chain: []InboxChainEntry{{ID: "c1", Kind: ChainReceived, Text: "收到 1 封邮件"},
			triagedEntry(i18n.ZhHans, &InboxItem{Tier: TierNoise, Reason: "订阅邮件"})}}
	it.Chain[1].ID = "c2"
	raw, err := json.Marshal(it.listed())
	if err != nil {
		t.Fatal(err)
	}
	out := i18n.InboxJSON(i18n.En, raw)
	var got struct {
		Chain []InboxChainEntry `json:"chain"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	if len(got.Chain) != 2 {
		t.Fatalf("chain: %+v", got.Chain)
	}
	if text := got.Chain[1].Text; text != "Sorted: Folded" {
		t.Errorf("triaged text for an English reader: %q, want %q", text, "Sorted: Folded")
	}
	if !json.Valid(out) || got.Chain[0].Text != "1 email" {
		t.Errorf("received text: %q", got.Chain[0].Text)
	}
}

// The same entry read in Chinese is unchanged (written in Chinese, read in Chinese).
func TestInboxChainTriagedTextStaysChinese(t *testing.T) {
	e := triagedEntry(i18n.ZhHans, &InboxItem{Tier: TierNoise})
	if e.Text != "分拣：已折叠" {
		t.Fatalf("written in Chinese: %q", e.Text)
	}
	raw, _ := json.Marshal(itWithChain(e))
	if got := chainTexts(t, i18n.InboxJSON(i18n.ZhHans, raw)); len(got) != 1 || got[0] != "分拣：已折叠" {
		t.Fatalf("Chinese reader: %+v", got)
	}
}

func itWithChain(es ...InboxChainEntry) InboxItem {
	it := InboxItem{ID: "chain-lang", Source: "mail", Kind: KindMail, Tier: TierNoise, Chain: es}
	return it.listed()
}

func chainTexts(t *testing.T, raw []byte) []string {
	t.Helper()
	var got struct {
		Chain []InboxChainEntry `json:"chain"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	out := make([]string, 0, len(got.Chain))
	for _, e := range got.Chain {
		out = append(out, e.Text)
	}
	return out
}
