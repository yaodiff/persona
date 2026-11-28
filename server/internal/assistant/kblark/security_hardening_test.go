package kblark

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/cliwire"
	"github.com/nwverse/persona/server/internal/kb"
)

func TestIsLarkLinkRequiresHTTPSAndCanonicalLarkHost(t *testing.T) {
	allowed := []string{
		"https://x.larkoffice.com/docx/d1",
		"HTTPS://x.larkoffice.com/docx/d1",
		"https://x.larkoffice.com:443/docx/d1",
		"https://example.feishu.cn/wiki/w1",
		"https://team.larksuite.com/sheets/s1",
		"https://feishu.cn/docx/d2",
		"https://larkoffice.com/minutes/m1",
		"https://larksuite.com/base/b1",
		"https://X.LARKOFFICE.COM./docx/d3",
	}
	for _, u := range allowed {
		if !isLarkLink(&kb.Item{URL: u}) {
			t.Errorf("isLarkLink rejected valid Lark URL %q", u)
		}
	}

	rejected := []string{
		"",
		" https://x.larkoffice.com/docx/d1",
		"https://x.larkoffice.com/docx/d1 ",
		"http://x.larkoffice.com/docx/d1",
		"https://x.larkoffice.com:8443/docx/d1",
		"https://x.larkoffice.com:/docx/d1",
		"https://.feishu.cn/docx/d1",
		"https://a..feishu.cn/docx/d1",
		"https://x.larkoffice.com../docx/d1",
		"https://-bad.feishu.cn/docx/d1",
		"https://bad-.feishu.cn/docx/d1",
		"https://evil;.feishu.cn/docx/d1",
		"https://x.larkoffice.com\\@evil.example/docx/d1",
		"https://x.larkoffice.com/docx/d1\nfoo",
		"https://x.larkoffice.com/docx/d1\x00",
		"https://evil.example/phish?x=feishu",
		"https://evil.example/lark/docx/d1",
		"https://feishu.evil.example/docx/d1",
		"https://evil-larkoffice.com/docx/d1",
		"https://larksuite.com.evil.example/docx/d1",
		"https://feishu.cn@evil.example/docx/d1",
		"https://user:pass@" + "x.larkoffice.com/docx/d1",
		"javascript:feishu.cn/docx/d1",
		"file:///etc/feishu",
		"--config=feishu",
	}
	for _, u := range rejected {
		if isLarkLink(&kb.Item{URL: u}) {
			t.Errorf("isLarkLink accepted non-Lark/unsafe URL %q", u)
		}
	}
	if isLarkLink(nil) {
		t.Error("isLarkLink(nil) = true, want false")
	}
}

type recordingRunner struct {
	calls [][]string
	reply func(call int, args []string) ([]byte, error)
}

func (r *recordingRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	cp := append([]string(nil), args...)
	r.calls = append(r.calls, cp)
	return r.reply(len(r.calls), cp)
}

func (r *recordingRunner) InspectDocument(context.Context, string) (cliwire.Document, error) {
	return cliwire.Document{}, nil
}

func TestClientHardeningPaginationDateTimezoneAndFlagInjection(t *testing.T) {
	t.Run("SearchMinutes stops on repeated page_token and accepts base64-like page_token", func(t *testing.T) {
		b64Token := "cGFnZV8y+a/b:c=1_2-3"
		rr := &recordingRunner{
			reply: func(call int, _ []string) ([]byte, error) {
				if call == 1 {
					return []byte(`{"ok":true,"data":{"items":[{"token":" t1 ","display_info":"Weekly"}],"has_more":true,"page_token":"` + b64Token + `"}}`), nil
				}
				return []byte(`{"ok":true,"data":{"items":[{"token":"t1","display_info":"Weekly"},{"token":"t2","display_info":"Sync"}],"has_more":true,"page_token":"` + b64Token + `"}}`), nil
			},
		}
		c := NewClient(rr, time.UTC)
		minutes, err := c.SearchMinutes(context.Background(), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatalf("SearchMinutes: %v", err)
		}
		if len(rr.calls) != 2 || len(minutes) != 2 || minutes[0].Token != "t1" || minutes[1].Token != "t2" {
			t.Fatalf("calls=%d minutes=%+v, want 2 calls and deduplicated [t1, t2]", len(rr.calls), minutes)
		}
		if !slices.Contains(rr.calls[1], "--page-token="+b64Token) {
			t.Fatalf("page 2 args = %v, want --page-token=%s", rr.calls[1], b64Token)
		}
	})

	t.Run("SearchMinutes rejects leading-dash page_token and honors canceled ctx", func(t *testing.T) {
		rr := &recordingRunner{
			reply: func(_ int, _ []string) ([]byte, error) {
				return []byte(`{"ok":true,"data":{"items":[{"token":"t1","display_info":"Weekly"}],"has_more":true,"page_token":"--evil-flag"}}`), nil
			},
		}
		c := NewClient(rr, time.UTC)
		minutes, err := c.SearchMinutes(context.Background(), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatalf("SearchMinutes: %v", err)
		}
		if len(rr.calls) != 1 || len(minutes) != 1 {
			t.Fatalf("expected SearchMinutes to stop before passing leading-dash page_token: calls=%v", rr.calls)
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := c.SearchMinutes(ctx, time.Now().AddDate(0, 0, -1), time.Now()); err == nil {
			t.Fatal("expected error for canceled context in SearchMinutes")
		}
		if _, err := c.InspectDoc(ctx, "https://x.larkoffice.com/docx/d1"); err == nil {
			t.Fatal("expected error for canceled context in InspectDoc")
		}
	})

	t.Run("MinuteDetail rejects empty, flag-like, whitespace, and comma-batch tokens", func(t *testing.T) {
		rr := &recordingRunner{
			reply: func(_ int, _ []string) ([]byte, error) {
				return []byte(`{"ok":true,"data":{"minutes":[{"title":"x"}]}}`), nil
			},
		}
		c := NewClient(rr, time.UTC)
		for _, bad := range []string{"", "-x", "--profile=evil", "tok with space", "tok\nline", "t1,t2"} {
			if _, err := c.MinuteDetail(context.Background(), bad); err == nil {
				t.Errorf("MinuteDetail accepted unsafe token %q", bad)
			}
		}
		if len(rr.calls) != 0 {
			t.Fatalf("runner invoked for invalid tokens: %v", rr.calls)
		}
	})

	t.Run("SearchDocs binds --query=value safely and parses zoneless/explicit-zone timestamps", func(t *testing.T) {
		tokyo := time.FixedZone("UTC+9", 9*3600)
		rr := &recordingRunner{
			reply: func(_ int, _ []string) ([]byte, error) {
				return []byte(`{"ok":true,"data":{"results":[` +
					`{"result_meta":{"token":" d1 ","url":"https://x.larkoffice.com/docx/d1","update_time_iso":"2026-11-20 09:52:58"},"title_highlighted":"Plan"},` +
					`{"result_meta":{"token":"d2","url":"https://x.larkoffice.com/docx/d2","update_time_iso":"2026-11-20T09:52:58Z"},"title_highlighted":"UTC"},` +
					`{"result_meta":{"token":"d3","url":"https://x.larkoffice.com/docx/d3","update_time_iso":"2026-11-20T09:52:58+08:00"},"title_highlighted":"CST"}` +
					`]}}`), nil
			},
		}
		c := NewClient(rr, tokyo)
		docs, err := c.SearchDocs(context.Background(), "--flag-like-query")
		if err != nil {
			t.Fatalf("SearchDocs: %v", err)
		}
		if len(rr.calls) != 1 || !slices.Contains(rr.calls[0], "--query=--flag-like-query") {
			t.Fatalf("SearchDocs did not bind query with --query=...: %v", rr.calls)
		}
		if len(docs) != 3 || docs[0].Token != "d1" {
			t.Fatalf("docs = %+v, want 3 docs with trimmed d1", docs)
		}
		wantTokyo := time.Date(2026, 11, 20, 9, 52, 58, 0, tokyo)
		if !docs[0].At.Equal(wantTokyo) || docs[0].At.Location() != tokyo {
			t.Fatalf("zoneless doc.At = %v (%v), want %v (%v)", docs[0].At, docs[0].At.Location(), wantTokyo, tokyo)
		}
		if !docs[1].At.Equal(time.Date(2026, 11, 20, 9, 52, 58, 0, time.UTC)) {
			t.Fatalf("UTC doc.At = %v", docs[1].At)
		}
		if !docs[2].At.Equal(time.Date(2026, 11, 20, 1, 52, 58, 0, time.UTC)) {
			t.Fatalf("+08:00 doc.At = %v", docs[2].At)
		}

		// Zero-value Client (nil zone) falls back to UTC for zoneless timestamps.
		if got := (&Client{}).parseISO("2026-11-20 09:52:58"); !got.Equal(time.Date(2026, 11, 20, 9, 52, 58, 0, time.UTC)) || got.Location() != time.UTC {
			t.Fatalf("nil-zone parseISO = %v (%v)", got, got.Location())
		}
	})

	t.Run("parseStart validates leap years and rejects invalid calendar dates", func(t *testing.T) {
		c := NewClient(nil, time.UTC)
		for _, bad := range []string{
			"Start: 2026-02-31 10:00:00",
			"Start: 2026-02-29 10:00:00",
			"Start: 2026-04-31 10:00:00",
		} {
			if got := c.parseStart(bad); !got.IsZero() {
				t.Errorf("parseStart(%q) = %v, want zero time", bad, got)
			}
		}
		leap := c.parseStart("Start: 2024-02-29 10:00:00")
		wantLeap := time.Date(2024, 2, 29, 10, 0, 0, 0, time.UTC)
		if !leap.Equal(wantLeap) {
			t.Fatalf("parseStart(2024-02-29) = %v, want %v", leap, wantLeap)
		}
		valid := c.parseStart("Bad: 2026-02-31 10:00:00", "Real: 2026-02-28 10:00:00")
		want := time.Date(2026, 2, 28, 10, 0, 0, 0, time.UTC)
		if !valid.Equal(want) {
			t.Fatalf("parseStart valid fallback = %v, want %v", valid, want)
		}
	})
}

func TestRunSkipsBlankDocTokensTrimsTokensAndRejectsForeignLinks(t *testing.T) {
	e := newEngine(t)
	if err := e.Store.PutItem(&kb.Item{Kind: kb.KindDoc, Source: "other", ID: "evil", URL: "https://evil.example/?q=feishu"}); err != nil {
		t.Fatal(err)
	}
	r := &fakeReader{
		minutes:  []Minute{{Token: "  ", Title: "blank"}, {Token: " m1 ", Title: "Meet", URL: "https://x.larkoffice.com/minutes/m1"}},
		detail:   map[string]Minute{"m1": {Summary: "Trimmed ok"}},
		detailOK: true,
		docs:     map[string]string{"https://evil.example/?q=feishu": "SHOULD NOT INSPECT"},
		searchOK: true,
		search: map[string][]Doc{
			"q": {
				{Token: "   ", Title: "blank-id", URL: "https://x.larkoffice.com/docx/blank"},
				{Token: " d1 ", Title: "good", URL: "https://x.larkoffice.com/docx/d1"},
			},
		},
	}
	res, err := Run(context.Background(), e, r, Config{DocSearch: []string{"q"}}, time.Now(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Filled != 0 {
		t.Fatalf("Filled = %d, want 0 (foreign URL containing 'feishu' must not be inspected)", res.Filled)
	}
	if res.Docs != 1 {
		t.Fatalf("Docs = %d, want 1 (blank token must be skipped)", res.Docs)
	}
	if it := meeting(t, e, "lark-minutes-m1"); !strings.Contains(it.Note, "Trimmed ok") {
		t.Fatalf("trimmed minute note = %q", it.Note)
	}
	if it, ok := e.Store.Item(kb.KindDoc, "d1"); !ok || it.ID != "d1" {
		t.Fatalf("trimmed doc d1 not stored: ok=%v it=%+v", ok, it)
	}
	for _, it := range e.Store.Items(kb.KindDoc) {
		if strings.TrimSpace(it.ID) == "" {
			t.Fatalf("blank doc ID was persisted: %+v", it)
		}
	}
}
