package kb

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The Moments archive (docs/assistant/knowledge.md "Pipeline", All Moments): every post the crawler reaches, kept as text under
// <data>/moments, one Markdown file per post (the post, then its comments) and its metadata beside it. Archive only:
// nothing here goes to the model post by post; the posts that concern me are chats (moments-<id>) as well, summarized
// like any chat. Search reads these files after everything else.

// MomentPost is a post as archived.
type MomentPost struct {
	ID           string    `json:"id"`
	Author       string    `json:"author"`
	AuthorID     string    `json:"author_id,omitempty"`
	Anonymous    bool      `json:"anonymous,omitempty"`
	Categories   []string  `json:"categories,omitempty"`
	Text         string    `json:"text"`
	Images       int       `json:"images,omitempty"`
	Reactions    int       `json:"reactions,omitempty"`
	CommentCount int       `json:"comment_count"`
	At           time.Time `json:"at"`
	SeenAt       time.Time `json:"seen_at"`               // when the crawler last saw it in a feed
	CommentsOf   int       `json:"comments_of,omitempty"` // CommentCount when the comments were last read; -1: never
	CommentsAt   time.Time `json:"comments_at,omitempty"`
	Mine         bool      `json:"mine,omitempty"`     // my post
	Concerns     bool      `json:"concerns,omitempty"` // it is a chat as well (mine, or someone commented / @ed me)
	// Source is the connector that keeps it ("" before connectors could: the hub's own crawler's); Feed is what
	// the feed is called, for the heading ("" is "posts").
	Source string `json:"source,omitempty"`
	Feed   string `json:"feed,omitempty"`
}

// MomentComment is a comment as archived.
type MomentComment struct {
	Author  string    `json:"author"`
	ReplyTo string    `json:"reply_to,omitempty"` // whom it answers
	Text    string    `json:"text"`
	At      time.Time `json:"at"`
}

func (s *Store) momentsDir() string { return filepath.Join(s.Dir, "moments", "posts") }

// MomentPost reads one archived post's metadata.
func (s *Store) MomentPost(id string) (*MomentPost, bool) {
	var p MomentPost
	ok, err := readJSON(filepath.Join(s.momentsDir(), SafeID(id)+".json"), &p)
	if !ok || err != nil {
		return nil, false
	}
	return &p, true
}

// PutMomentPost writes a post's metadata, and its Markdown when comments are given (nil keeps the comments the
// file already has: a feed pass only refreshes counts).
func (s *Store) PutMomentPost(p *MomentPost, comments []MomentComment) error {
	if s.readOnly {
		return ErrReadOnly
	}
	dir := s.momentsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	md := filepath.Join(dir, SafeID(p.ID)+".md")
	if comments != nil {
		if err := writeFile(md, []byte(momentMarkdown(p, comments, s.Zone()))); err != nil {
			return err
		}
	} else if _, err := os.Stat(md); os.IsNotExist(err) {
		if err := writeFile(md, []byte(momentMarkdown(p, nil, s.Zone()))); err != nil {
			return err
		}
	}
	return writeJSON(filepath.Join(dir, SafeID(p.ID)+".json"), p)
}

// MomentPosts lists every archived post's metadata.
func (s *Store) MomentPosts() []*MomentPost {
	ents, err := os.ReadDir(s.momentsDir())
	if err != nil {
		return nil
	}
	var out []*MomentPost
	for _, e := range ents {
		if id, ok := strings.CutSuffix(e.Name(), ".json"); ok {
			if p, ok := s.MomentPost(id); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

// WriteMomentIndex rewrites moments/index.jsonl: one line per post, newest first (for rg and quick lists).
func (s *Store) WriteMomentIndex() (int, error) {
	if s.readOnly {
		return 0, ErrReadOnly
	}
	ps := s.MomentPosts()
	sort.Slice(ps, func(i, j int) bool { return ps[i].At.After(ps[j].At) })
	var b strings.Builder
	for _, p := range ps {
		line, _ := json.Marshal(map[string]any{"id": p.ID, "at": p.At.Format(time.RFC3339), "author": p.Author,
			"categories": p.Categories, "comments": p.CommentCount, "reactions": p.Reactions, "concerns": p.Concerns,
			"text": clip(oneLine(p.Text), 120)})
		b.Write(line)
		b.WriteByte('\n')
	}
	return len(ps), writeFile(filepath.Join(s.Dir, "moments", "index.jsonl"), []byte(b.String()))
}

// PruneMoments deletes posts written before cutoff (text retention).
func (s *Store) PruneMoments(cutoff time.Time) int {
	if s.readOnly {
		return 0
	}
	n := 0
	for _, p := range s.MomentPosts() {
		if p.At.Before(cutoff) {
			base := filepath.Join(s.momentsDir(), SafeID(p.ID))
			_ = os.Remove(base + ".md")
			if os.Remove(base+".json") == nil {
				n++
			}
		}
	}
	if n > 0 {
		s.SearchIndexPurge()
	}
	return n
}

func momentMarkdown(p *MomentPost, comments []MomentComment, zone *time.Location) string {
	var b strings.Builder
	title := clip(oneLine(p.Text), 40)
	if title == "" {
		title = "（没有文字）"
	}
	feed := p.Feed
	if feed == "" {
		feed = "帖子"
	}
	fmt.Fprintf(&b, "# %s · %s\n\n", feed, title)
	who := p.Author
	if p.Anonymous || who == "" {
		who = "匿名"
	}
	meta := []string{who, p.At.In(zone).Format("2006-01-02 15:04")}
	if len(p.Categories) > 0 {
		meta = append(meta, strings.Join(p.Categories, "、"))
	}
	meta = append(meta, fmt.Sprintf("赞 %d", p.Reactions), fmt.Sprintf("评论 %d", p.CommentCount))
	if p.Images > 0 {
		meta = append(meta, fmt.Sprintf("图 %d", p.Images))
	}
	fmt.Fprintf(&b, "%s · 帖子 %s\n\n%s\n", strings.Join(meta, " · "), p.ID, strings.TrimSpace(p.Text))
	if comments == nil {
		if p.CommentCount > 0 {
			b.WriteString("\n（评论还没读）\n")
		}
		return b.String()
	}
	fmt.Fprintf(&b, "\n## 评论（%d）\n\n", len(comments))
	for _, c := range comments {
		who := c.Author
		if who == "" {
			who = "匿名"
		}
		to := ""
		if c.ReplyTo != "" {
			to = "（回复 " + c.ReplyTo + "）"
		}
		fmt.Fprintf(&b, "- %s %s: %s%s\n", c.At.In(zone).Format("01-02 15:04"), who, to, strings.Join(strings.Fields(c.Text), " "))
	}
	return b.String()
}

// MomentArchived is one archived post and its Markdown (text and comments), for reading it in the app.
func (s *Store) MomentArchived(id string) (*MomentPost, string, bool) {
	p, ok := s.MomentPost(id)
	if !ok {
		return nil, "", false
	}
	raw, _ := os.ReadFile(filepath.Join(s.momentsDir(), SafeID(id)+".md"))
	return p, string(raw), true
}
