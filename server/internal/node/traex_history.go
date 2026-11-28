package node

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"sync"

	"github.com/nwverse/persona/server/internal/proto"
)

type traexHistoryRevisionCache struct {
	stamp    string
	byThread map[string]string
}

var traexHistoryRevisions = struct {
	sync.Mutex
	byDB map[string]traexHistoryRevisionCache
}{byDB: map[string]traexHistoryRevisionCache{}}

// Read all threads' small projection markers once per DB/WAL change, including changes to active turn state.
// A missing revision or failed read keeps the whole database stamp so older schemas still refresh their history.
func traexHistoryStamp(db, thread, stamp string) string {
	traexHistoryRevisions.Lock()
	defer traexHistoryRevisions.Unlock()
	c, ok := traexHistoryRevisions.byDB[db]
	if !ok || c.stamp != stamp {
		c = traexHistoryRevisionCache{stamp: stamp, byThread: map[string]string{}}
		var rows []struct {
			ThreadID           string  `json:"thread_id"`
			NextRolloutOrdinal *int64  `json:"next_rollout_ordinal"`
			ActiveTurnID       *string `json:"active_turn_id"`
		}
		if err := querySQLite(context.Background(), db,
			`SELECT thread_id, next_rollout_ordinal, active_turn_id FROM thread_history_projection_state`, &rows); err == nil {
			for _, row := range rows {
				if row.ThreadID == "" || row.NextRolloutOrdinal == nil || *row.NextRolloutOrdinal < 0 {
					continue
				}
				// Include active state even when the ordinal has not advanced, without reading projection context.
				if revision, err := json.Marshal(row); err == nil {
					c.byThread[row.ThreadID] = "projection:" + db + ":" + string(revision)
				}
			}
		}
		traexHistoryRevisions.byDB[db] = c
	}
	if revision, ok := c.byThread[thread]; ok {
		return revision
	}
	return stamp
}

// TRAE CLI 0.215 stores new conversations in state_<n>.sqlite and thread_history_<n>.sqlite, without a rollout.
// Reuse the paginated Codex reader: both CLIs store the same ThreadItems. Writer locks still identify the exact
// process and conversation. Subagent histories never become independent terminal conversations.
func (n *Node) traexThreads() map[string]codexAppThread {
	return traexThreadsForHome(n.traexHome())
}

// traexThreadsForHome is codexPaginatedThreads, indirected so tests can count TRAE snapshot rebuilds.
var traexThreadsForHome = codexPaginatedThreads

// traexThreadIn is n.traexThread with the home's paginated snapshot memoized once per list pass (codexListPass): many
// rows asking for a TRAE id then take one snapshot instead of re-acquiring its lock and map each time. A nil pass
// resolves the snapshot directly, exactly as n.traexThread does.
func (n *Node) traexThreadIn(id string, pass *codexListPass) (codexAppThread, bool) {
	t, ok := pass.paginatedThreadOf(codexSource{home: n.traexHome(), thread: id})
	if !ok {
		return codexAppThread{}, false
	}
	t.includeRunning = true
	return t, traexConversation(t)
}

func traexConversation(t codexAppThread) bool {
	var source map[string]json.RawMessage
	return validID(t.ID) && t.Source != "subagent" &&
		!(json.Unmarshal([]byte(t.Source), &source) == nil && source["subagent"] != nil)
}

func (n *Node) traexThread(id string) (codexAppThread, bool) {
	t, ok := n.traexThreads()[id]
	t.includeRunning = true
	return t, ok && traexConversation(t)
}

// traexRefreshHistory updates the projected transcript for a live or recently stopped paginated TRAE
// conversation so final reply text and completion timestamps stay attached without projecting cold history.
func (n *Node) traexRefreshHistory(id string) {
	if !validID(id) || n.codexAppHistoryDir() == "" {
		return
	}
	codexPaginatedThreadsFresh(n.traexHome())
	_ = n.refreshCodexAppHistory(id)
}

// Keep a conversation readable and listed after its terminal exits, as for rollout-backed agents.
func (n *Node) traexSessions(seen map[string]bool) []proto.Session {
	var out []proto.Session
	for id, t := range n.traexThreads() {
		if seen[id] || !traexConversation(t) {
			continue
		}
		s := proto.Session{SessionID: id, Agent: traexAgent, Title: t.Title, Cwd: t.Cwd, Model: t.Model,
			Status: proto.StatusOffline, UpdatedAt: t.updated(), CodexRoute: codexRouteTerminal}
		if p := n.traexPath(id); p != "" {
			if st, err := os.Stat(p); err == nil {
				m := n.meta(p, st)
				s.LastText, s.Rev = snippet(m.lastText), st.Size()
				if !m.activity.IsZero() && (s.UpdatedAt.IsZero() || m.activity.After(s.UpdatedAt)) {
					s.UpdatedAt = m.activity.UTC()
				}
				m.turnErr.set(&s)
			}
		}
		n.decorate(&s)
		out = append(out, s)
	}
	// The final list's stable time sort must not inherit random map order for ties.
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}
