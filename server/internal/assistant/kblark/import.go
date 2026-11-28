package kblark

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
)

const (
	// Source is the item source the import writes: the manager's local Lark access, not the hub's larkx connector.
	Source = "larkcli"
	// Where is what the items say they came from.
	Where = "lark-cli"
	// minutesPrefix makes a minute's id: one per lark-cli token, so a re-import updates rather than adds.
	minutesPrefix = "lark-minutes-"
	// defaultSinceDays is how far back a run looks when kb.lark_minutes.since_days is not set.
	defaultSinceDays = 30
	// titlesPerRun is how many document titles a run fills, as the connectors' item collection does.
	titlesPerRun = 10
)

// Config is what a run goes by (kb.lark_minutes).
type Config struct {
	SinceDays int
	DocSearch []string
	// Lang is the owner's language, which a minute's note words its labels in (zero: i18n.Default). A note keeps the
	// language it was written in: the import writes a note only while there is none.
	Lang i18n.Lang
}

// Result is what a run did.
type Result struct {
	Minutes int // minutes the search returned
	Changed int // items written (new or updated)
	Docs    int // documents found by DocSearch and written
	Filled  int // document titles filled
}

// Run imports the owner's minutes and fills document titles into e, through r. A failed search is returned; a minute
// whose detail cannot be read is written without its note (a later run tries again), and a document title that cannot
// be had waits for a later run. logf may be nil.
func Run(ctx context.Context, e *kb.Engine, r Reader, cfg Config, now time.Time, logf func(string, ...any)) (Result, error) {
	var res Result
	since := cfg.SinceDays
	if since <= 0 {
		since = defaultSinceDays
	}
	minutes, err := r.SearchMinutes(ctx, now.AddDate(0, 0, -since), now)
	if err != nil {
		return res, err
	}
	res.Minutes = len(minutes)
	for _, m := range minutes {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		tok := strings.TrimSpace(m.Token)
		if tok == "" {
			continue
		}
		id := minutesPrefix + tok
		// Read the detail only while the note is missing: an imported minute keeps it, a user's edit included.
		if old, ok := e.Store.Item(kb.KindMeeting, id); !ok || old.Note == "" {
			d, derr := r.MinuteDetail(ctx, tok)
			if derr != nil {
				if logf != nil {
					logf("kb: lark minutes: reading a minute's detail: %v", derr)
				}
			} else {
				if m.Title == "" {
					m.Title = d.Title
				}
				m.Summary, m.Todos, m.Chapters = d.Summary, d.Todos, d.Chapters
			}
		}
		it := kb.Item{Kind: kb.KindMeeting, Source: Source, ID: id, Title: m.Title, URL: m.URL, At: m.Start,
			Where: Where, Note: minuteNote(m, cfg.Lang)}
		changed, err := e.PutCollectedItem(&it)
		if err != nil {
			return res, err
		}
		if changed {
			res.Changed++
		}
	}
	failedTitles := 0
	res.Filled = e.FillDocTitles(ctx, titlesPerRun, func(ctx context.Context, url string) (string, error) {
		title, err := r.InspectDoc(ctx, url)
		if err != nil {
			failedTitles++
		}
		return title, err
	}, isLarkLink)
	if failedTitles > 0 && logf != nil {
		logf("kb: lark minutes: %d document title(s) could not be read", failedTitles)
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	for _, q := range cfg.DocSearch {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		if q = strings.TrimSpace(q); q == "" {
			continue
		}
		docs, err := r.SearchDocs(ctx, q)
		if err != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			if logf != nil {
				logf("kb: lark minutes: searching documents: %v", err)
			}
			continue
		}
		for _, d := range docs {
			tok := strings.TrimSpace(d.Token)
			if tok == "" {
				continue
			}
			it := kb.Item{Kind: kb.KindDoc, Source: Source, ID: tok, Title: d.Title, URL: d.URL, At: d.At, Where: Where}
			changed, err := e.PutCollectedItem(&it)
			if err != nil {
				return res, err
			}
			if changed {
				res.Docs++
			}
		}
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	return res, nil
}

// minuteNote is what the item's note keeps of a minute: one line of its summary, its first few to-dos and chapter
// titles — never the transcript. Its labels are in lang (the kb-lark catalog); what Lark sent is kept as it came.
func minuteNote(m Minute, lang i18n.Lang) string {
	var b strings.Builder
	if s := oneLine(m.Summary); s != "" {
		b.WriteString(i18n.T(lang, "kb-lark.summary"))
		b.WriteString(clip(s, 240))
	}
	add := func(label string, list []string, most, each int) {
		var parts []string
		for _, v := range list {
			if v = oneLine(v); v != "" {
				parts = append(parts, clip(v, each))
			}
			if len(parts) == most {
				break
			}
		}
		if len(parts) == 0 {
			return
		}
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString(label)
		b.WriteString(strings.Join(parts, i18n.T(lang, "kb-lark.list_sep")))
	}
	add(i18n.T(lang, "kb-lark.todos"), m.Todos, 3, 80)
	add(i18n.T(lang, "kb-lark.chapters"), m.Chapters, 5, 40)
	return clip(b.String(), 700)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func validDNSName(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
				return false
			}
		}
	}
	return true
}

// isLarkLink: an HTTPS Feishu/Lark document URL lark-cli can inspect; foreign or non-HTTPS links are left alone.
func isLarkLink(it *kb.Item) bool {
	if it == nil {
		return false
	}
	raw := it.URL
	if raw == "" || strings.TrimSpace(raw) != raw || strings.ContainsRune(raw, '\\') {
		return false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < 0x20 || raw[i] == 0x7f {
			return false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || strings.HasSuffix(u.Host, ":") {
		return false
	}
	if port := u.Port(); port != "" && port != "443" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if !validDNSName(host) {
		return false
	}
	for _, domain := range []string{"feishu.cn", "larksuite.com", "larkoffice.com"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}
