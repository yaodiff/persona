// Package kblark imports the owner's Lark (Feishu) meeting minutes and document titles into the knowledge base
// through lark-cli, the same login the Lark connector uses (docs/assistant/knowledge.md "Lark minutes and documents").
// It runs inside the manager's knowledge-base round and never stores a transcript: a minute keeps one line of its
// summary, its first few to-dos and chapter titles.
package kblark

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/cliwire"
	"github.com/nwverse/persona/server/internal/lazyre"
)

const (
	// maxSearchPages bounds one minutes search: at most this many pages of pageSize.
	maxSearchPages = 20
	// pageSize is the page size asked of minutes +search (its maximum is 30).
	pageSize = 30
)

// Minute is one meeting minute: what the search lists, and what the detail adds.
type Minute struct {
	Token    string
	Title    string
	URL      string
	Start    time.Time
	Summary  string
	Todos    []string
	Chapters []string
}

// Doc is one document a keyword search found.
type Doc struct {
	Token string
	Title string
	URL   string
	At    time.Time
}

// Reader is what an import reads: minutes and documents through lark-cli (Client), or a fake in tests.
type Reader interface {
	SearchMinutes(ctx context.Context, start, end time.Time) ([]Minute, error)
	MinuteDetail(ctx context.Context, token string) (Minute, error)
	InspectDoc(ctx context.Context, url string) (string, error)
	SearchDocs(ctx context.Context, query string) ([]Doc, error)
}

// Runner is what a client runs lark-cli through: the lark module's adapter, which the core reaches through its port
// (cliport.Runner, internal/cliport), or a fake in tests.
type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
	InspectDocument(ctx context.Context, link string) (cliwire.Document, error)
}

// Client reads through lark-cli, as the logged-in user.
type Client struct {
	runner Runner
	zone   *time.Location
}

// NewClient returns a client on runner; a wall-clock time lark-cli prints without a zone is read in zone. Every caller
// passes the knowledge base's zone (kb.Store.Zone, never nil). A nil zone is read as UTC only as a defensive
// fallback, never the machine's zone: the hub runs in UTC and its user does not (internal/tzguard).
func NewClient(runner Runner, zone *time.Location) *Client {
	if zone == nil {
		zone = time.UTC
	}
	return &Client{runner: runner, zone: zone}
}

func validCLIToken(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// SearchMinutes lists the minutes the owner took part in between start and end, newest pages until the last.
func (c *Client) SearchMinutes(ctx context.Context, start, end time.Time) ([]Minute, error) {
	var out []Minute
	seenTokens := map[string]bool{}
	seenMinutes := map[string]bool{}
	token := ""
	for page := 0; page < maxSearchPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		args := []string{"minutes", "+search", "--participant-ids", "me",
			"--start", start.Format("2006-01-02"), "--end", end.Format("2006-01-02"), "--page-size", strconv.Itoa(pageSize)}
		if token != "" {
			args = append(args, "--page-token="+token)
		}
		raw, err := c.runner.Run(ctx, args...)
		if err != nil {
			return nil, err
		}
		var env struct {
			Data struct {
				Items []struct {
					Token       string `json:"token"`
					DisplayInfo string `json:"display_info"`
					Meta        struct {
						AppLink     string `json:"app_link"`
						Description string `json:"description"`
					} `json:"meta_data"`
				} `json:"items"`
				HasMore   bool   `json:"has_more"`
				PageToken string `json:"page_token"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("minutes search: %w", err)
		}
		for _, it := range env.Data.Items {
			tok := strings.TrimSpace(it.Token)
			if tok == "" || seenMinutes[tok] {
				continue
			}
			seenMinutes[tok] = true
			out = append(out, Minute{
				Token: tok,
				Title: minuteTitle(it.DisplayInfo),
				URL:   it.Meta.AppLink,
				Start: c.parseStart(it.Meta.Description, it.DisplayInfo),
			})
		}
		next := env.Data.PageToken
		if !env.Data.HasMore || !validCLIToken(next) || seenTokens[next] {
			break
		}
		seenTokens[next] = true
		token = next
	}
	return out, nil
}

// MinuteDetail reads one minute's title, summary, to-dos and chapters (never its transcript).
func (c *Client) MinuteDetail(ctx context.Context, token string) (Minute, error) {
	if err := ctx.Err(); err != nil {
		return Minute{}, err
	}
	if !validCLIToken(token) || strings.Contains(token, ",") {
		return Minute{}, fmt.Errorf("minute detail: invalid token")
	}
	raw, err := c.runner.Run(ctx, "minutes", "+detail", "--minute-tokens="+token, "--summary", "--todo", "--chapter")
	if err != nil {
		return Minute{}, err
	}
	var env struct {
		Data struct {
			Minutes []struct {
				Title     string `json:"title"`
				Artifacts struct {
					Summary string `json:"summary"`
					Todos   []struct {
						Content string `json:"content"`
					} `json:"todos"`
					Chapters []struct {
						Title string `json:"title"`
					} `json:"chapters"`
				} `json:"artifacts"`
			} `json:"minutes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return Minute{}, fmt.Errorf("minute detail: %w", err)
	}
	if len(env.Data.Minutes) == 0 {
		return Minute{}, fmt.Errorf("minute detail: not found")
	}
	m := env.Data.Minutes[0]
	out := Minute{Token: token, Title: strings.TrimSpace(m.Title), Summary: m.Artifacts.Summary}
	for _, t := range m.Artifacts.Todos {
		if s := oneLine(t.Content); s != "" {
			out.Todos = append(out.Todos, s)
		}
	}
	for _, ch := range m.Artifacts.Chapters {
		if s := oneLine(ch.Title); s != "" {
			out.Chapters = append(out.Chapters, s)
		}
	}
	return out, nil
}

// InspectDoc is a document's title ("" when lark-cli cannot tell).
func (c *Client) InspectDoc(ctx context.Context, url string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	d, err := c.runner.InspectDocument(ctx, url)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(d.Title), nil
}

// SearchDocs finds documents by a keyword: the first page only, one per result.
func (c *Client) SearchDocs(ctx context.Context, query string) ([]Doc, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := c.runner.Run(ctx, "docs", "+search", "--query="+query, "--page-size", "20")
	if err != nil {
		return nil, err
	}
	var env struct {
		Data struct {
			Results []struct {
				Meta struct {
					Token         string `json:"token"`
					URL           string `json:"url"`
					UpdateTime    int64  `json:"update_time"`
					UpdateTimeISO string `json:"update_time_iso"`
				} `json:"result_meta"`
				Title string `json:"title_highlighted"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("docs search: %w", err)
	}
	var out []Doc
	for _, r := range env.Data.Results {
		tok := strings.TrimSpace(r.Meta.Token)
		if tok == "" || strings.TrimSpace(r.Meta.URL) == "" {
			continue
		}
		at := c.parseISO(r.Meta.UpdateTimeISO)
		if at.IsZero() && r.Meta.UpdateTime > 0 {
			at = time.UnixMilli(r.Meta.UpdateTime)
		}
		out = append(out, Doc{Token: tok, Title: cleanText(r.Title), URL: r.Meta.URL, At: at})
	}
	return out, nil
}

var (
	htmlTag   = lazyre.New(`<[^>]*>`)
	startTime = lazyre.New(`(\d{4})[.\-/](\d{1,2})[.\-/](\d{1,2})[ T]+(\d{1,2}):(\d{2})(?::(\d{2}))?`)
)

// cleanText undoes the HTML lark-cli escapes and marks up in display and highlight fields.
func cleanText(s string) string {
	return strings.TrimSpace(htmlTag.ReplaceAllString(html.UnescapeString(s), ""))
}

// minuteTitle is the first line of a minute's display info: its title, before the keywords and owner.
func minuteTitle(display string) string {
	s := cleanText(display)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func (c *Client) loc() *time.Location {
	if c != nil && c.zone != nil {
		return c.zone
	}
	return time.UTC
}

// parseStart reads the start time out of the description ("… 开始时间: 2026.11.17 20:01:31 …") or the display info;
// zero when neither has one.
func (c *Client) parseStart(texts ...string) time.Time {
	zone := c.loc()
	for _, s := range texts {
		for _, m := range startTime.FindAllStringSubmatch(s, -1) {
			atoi := func(v string) int { n, _ := strconv.Atoi(v); return n }
			year, month, day := atoi(m[1]), atoi(m[2]), atoi(m[3])
			hour, minute, sec := atoi(m[4]), atoi(m[5]), atoi(m[6])
			if month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59 || sec > 59 {
				continue
			}
			utc := time.Date(year, time.Month(month), day, hour, minute, sec, 0, time.UTC)
			if utc.Year() != year || int(utc.Month()) != month || utc.Day() != day {
				continue
			}
			return time.Date(year, time.Month(month), day, hour, minute, sec, 0, zone)
		}
	}
	return time.Time{}
}

func (c *Client) parseISO(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, c.loc()); err == nil {
		return t
	}
	return time.Time{}
}
