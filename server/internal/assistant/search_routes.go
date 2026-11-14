package assistant

import (
	"context"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/chatsearch"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// GET /api/v1/assistant/search?q=&limit= (#6282, docs/protocol/protocol.md "Assistant chat search"): the conversations
// "All chats" lists whose title holds every word of q, or one of whose messages (the user's words, the assistant's
// replies: the index, search_index.go) does. Titles first, then by the newest hit; each with up to searchPerChat hits,
// newest first, cut to a snippet with its matches as UTF-16 ranges. The query is never logged.

const (
	searchPerChat      = 3
	searchLimit        = 30
	searchMaxLimit     = 50
	searchQueryTimeout = 2 * time.Second
)

type searchHit struct {
	ItemID  string    `json:"item_id"`
	Seq     int       `json:"seq"`
	Role    string    `json:"role"`
	TS      time.Time `json:"ts,omitzero"`
	Snippet string    `json:"snippet"`
	Ranges  [][2]int  `json:"ranges"`
}

type searchResult struct {
	SessionID  string      `json:"session_id"`
	MachineID  string      `json:"machine_id"`
	Title      string      `json:"title"`
	Main       bool        `json:"main,omitempty"`
	Archived   bool        `json:"archived,omitempty"`
	Origin     string      `json:"origin,omitempty"`
	UpdatedAt  time.Time   `json:"updated_at,omitzero"`
	TitleMatch bool        `json:"title_match,omitempty"`
	Hits       []searchHit `json:"hits"`
	MoreHits   int         `json:"more_hits,omitempty"`

	titleRank int       // 2: the title starts with the query, 1: holds every word
	newest    time.Time // the newest hit
}

type searchAnswer struct {
	Results []searchResult `json:"results"`
	// Partial: some conversations are not in the index yet (it is still reading them), or it could not be searched.
	Partial bool `json:"partial"`
	Indexed struct {
		Done  int `json:"done"`
		Total int `json:"total"`
	} `json:"indexed"`
}

func (s *Service) searchRoutes() {
	s.handle("GET /api/v1/assistant/search", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-search.forbidden"))
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = searchLimit
		}
		apierr.WriteJSON(w, s.searchChats(r.Context(), r.URL.Query().Get("q"), min(limit, searchMaxLimit)))
	})
}

// searchTitleOf is the title a conversation is found by: none for the main one (shown as Persona) or one not named yet.
func searchTitleOf(c proto.Session, main string) string {
	t := strings.TrimSpace(c.Title)
	if c.SessionID == main || t == "" || t == "Claude Code" || t == path.Base(c.Cwd) {
		return ""
	}
	return t
}

func (s *Service) searchChats(ctx context.Context, q string, limit int) searchAnswer {
	ans := searchAnswer{Results: []searchResult{}}
	list, main := s.searchConversations(ctx)
	ans.Indexed.Total = len(list)
	ans.Indexed.Done = s.searchProgress(list)
	ans.Partial = ans.Indexed.Done < ans.Indexed.Total
	words := chatsearch.Words(q)
	if len(words) == 0 {
		return ans
	}
	whole := strings.Join(words, " ")
	byKey := map[searchKey]*searchResult{}
	machines, sessions := make([]string, 0, len(list)), make([]string, 0, len(list))
	for _, c := range list {
		r := &searchResult{SessionID: c.SessionID, MachineID: c.MachineID, Title: c.Title, Main: c.SessionID == main,
			Archived: c.Archived, Origin: c.Origin, UpdatedAt: c.UpdatedAt, Hits: []searchHit{}}
		if t := chatsearch.Fold(searchTitleOf(c, main)); t != "" && chatsearch.Holds(t, words) {
			r.TitleMatch, r.titleRank = true, 1
			if strings.HasPrefix(t, whole) {
				r.titleRank = 2
			}
		}
		byKey[searchKey{c.MachineID, c.SessionID}] = r
		machines, sessions = append(machines, c.MachineID), append(sessions, c.SessionID)
	}
	if st := s.searchDB(); st != nil {
		qctx, cancel := context.WithTimeout(ctx, searchQueryTimeout)
		rows, err := st.SearchAssistantItems(qctx, machines, sessions, words, searchPerChat)
		cancel()
		if err != nil {
			s.logf("assistant: search: %v", err) // never the query itself
			ans.Partial = true
		}
		for _, row := range rows {
			r := byKey[searchKey{row.MachineID, row.SessionID}]
			if r == nil {
				continue
			}
			snippet, ranges := chatsearch.Snippet(row.Body, words)
			r.Hits = append(r.Hits, searchHit{ItemID: row.ItemID, Seq: row.Seq, Role: row.Role, TS: row.TS, Snippet: snippet, Ranges: ranges})
			r.MoreHits = max(0, row.Hits-searchPerChat)
			if row.TS.After(r.newest) {
				r.newest = row.TS
			}
		}
	} else {
		ans.Partial = true
	}
	var out []searchResult
	for _, r := range byKey {
		if r.TitleMatch || len(r.Hits) > 0 {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.titleRank != b.titleRank {
			return a.titleRank > b.titleRank
		}
		if a.titleRank > 0 && !a.UpdatedAt.Equal(b.UpdatedAt) { // titles: the latest updated first
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		if a.titleRank == 0 && !a.newest.Equal(b.newest) {
			return a.newest.After(b.newest)
		}
		return a.SessionID < b.SessionID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	if out != nil {
		ans.Results = out
	}
	return ans
}
