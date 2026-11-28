package node

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Codex panes on a shared app-server (docs/integrations/codex.md "Sessions on the daemon"). From 0.163 a plain `codex` runs its thread on
// the shared daemon (<CODEX_HOME>/app-server-control/app-server-control.sock) and its TUI is one client of it; the
// node keeps a connection to every app-server it finds (codex_remote.go). A herdr pane whose thread is loaded on one is
// joined — Persona becomes a second client of that thread, asking it for nothing and changing none of its settings —
// and from then on the thread's questions, approvals and the phone's messages go through the protocol: whoever answers
// first wins, and serverRequest/resolved takes the other side's card away. A pane that is not on one (a launcher with
// -c, a session running in its own process, 0.153 without a daemon, a compatible CLI) keeps the terminal route (codexask.go); so
// does a joined one whose app-server goes away, until it is back.

// Background join throttling and retry backoff (#13578): cap concurrent joins per app-server so an app-server restart
// with hundreds of resumable panes never stampedes the daemon, and space retries with capped exponential backoff + jitter.
var (
	codexJoinConcurrency = 4
	codexJoinRetry       = 30 * time.Second
	codexJoinRetryMax    = 5 * time.Minute
	codexJoinJitter      = defaultCodexJoinJitter
)

func defaultCodexJoinJitter(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	span := base / 4
	if span <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(span) + 1))
}

func codexJoinBackoff(fails int) time.Duration {
	base := codexJoinRetry
	if base <= 0 {
		return 0
	}
	shift := min(max(fails-1, 0), 4)
	wait := base << shift
	capWait := max(codexJoinRetryMax, base)
	if wait > capWait || wait < base {
		wait = capWait
	}
	if codexJoinJitter != nil {
		if extra := codexJoinJitter(wait); extra > 0 {
			wait += extra
		}
	}
	return wait
}

// Routes of a live Codex pane (proto.Session.CodexRoute).
const (
	codexRouteAppServer = "app_server"
	codexRouteTerminal  = "terminal"
)

type codexJoinState struct {
	busy     bool
	fails    int
	failedAt time.Time
	nextAt   time.Time
	note     string
}

// joinCodexPanes enqueues, on each owning app-server's bounded background worker pool, every live Codex pane whose
// thread runs on a connected app-server and is not joined yet, plus active or blocked threads on connected app-servers.
// Failed joins back off with jitter before retrying.
func (n *Node) joinCodexPanes(ctx context.Context) {
	if n == nil || ctx.Err() != nil || n.codices == nil {
		return
	}
	n.mu.Lock()
	var ids []string
	for id, a := range n.agents {
		// A botmux pane alone does not trigger a join because botmux delivers its own messages.
		if a.Agent == "codex" && a.PaneID != "" && host.ParsePane(a.PaneID).Kind != host.Botmux {
			ids = append(ids, id)
		}
	}
	n.mu.Unlock()
	n.codices.mu.Lock()
	remotes := slices.Collect(mapRemoteValues(n.codices.remotes))
	n.codices.mu.Unlock()
	for _, c := range remotes {
		c.mu.Lock()
		for _, t := range c.threads {
			if c.ready && !t.joined && (t.status == proto.StatusWorking || t.status == proto.StatusBlocked) {
				ids = append(ids, t.publicID)
			}
		}
		c.mu.Unlock()
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) == 0 {
		return
	}
	byRemote := map[*codexRemote][]string{}
	for _, id := range ids {
		c, t, err := n.codexOwner(id)
		if err != nil || t.joined || !n.cx.joinDue(id) {
			continue
		}
		byRemote[c] = append(byRemote[c], id)
	}
	for c, batch := range byRemote {
		c.enqueueBackgroundJoins(batch)
	}
}

func (c *codexCards) joinDue(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.joins[id]
	if s.busy {
		return false
	}
	if !s.nextAt.IsZero() {
		return !time.Now().Before(s.nextAt)
	}
	return s.failedAt.IsZero() || time.Since(s.failedAt) >= codexJoinRetry
}

func (c *codexCards) startJoin(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.joins == nil {
		c.joins = map[string]codexJoinState{}
	}
	s := c.joins[id]
	if s.busy {
		return false
	}
	now := time.Now()
	if !s.nextAt.IsZero() && now.Before(s.nextAt) {
		return false
	}
	if s.nextAt.IsZero() && !s.failedAt.IsZero() && now.Sub(s.failedAt) < codexJoinRetry {
		return false
	}
	s.busy = true
	c.joins[id] = s
	return true
}

func (c *codexCards) endJoin(id string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.joins == nil {
		c.joins = map[string]codexJoinState{}
	}
	if err == nil {
		c.joins[id] = codexJoinState{}
		return
	}
	now := time.Now()
	fails := c.joins[id].fails + 1
	wait := codexJoinBackoff(fails)
	c.joins[id] = codexJoinState{
		fails:    fails,
		failedAt: now,
		nextAt:   now.Add(wait),
		note:     "接入 app-server 失败：" + err.Error(),
	}
}

func (c *codexCards) joinNote(id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.joins[id].note
}

// codexRoute says how Persona reaches a live Codex pane now, and why when it is the terminal.
func (n *Node) codexRoute(id string) (route, note string) {
	_, t, err := n.codexOwner(id)
	switch {
	case err == nil && t.joined:
		return codexRouteAppServer, ""
	case err == nil && t.parent != "":
		return codexRouteTerminal, "子 Agent 由父 Agent 驱动"
	case err == nil:
		if note := n.cx.joinNote(id); note != "" {
			return codexRouteTerminal, note
		}
		return codexRouteTerminal, "正在接入它所在的 app-server"
	case errors.Is(err, errCodexNoOwner):
		if st, ok := n.state.get(id); ok && st.Launcher != "" {
			if argv, ok := n.codexArgv(st.Launcher); ok {
				if flag := codexOwnProcess(argv); flag != "" {
					return codexRouteTerminal, "用启动器「" + st.Launcher + "」起的：它带了 " + flag + "，Codex 这时在自己的进程里跑，不在共享的 app-server 上"
				}
			}
		}
		return codexRouteTerminal, "不在共享的 app-server 上（带 -c 的启动器、在自己进程里跑的会话、没有守护进程的 0.153）"
	}
	return codexRouteTerminal, err.Error()
}

// codexReplyRemote answers async questions of a joined thread: the same reply envelope as typed into its box
// (codexReply), sent as a message through the app-server — the TUI's box and anything drafted in it stay as they are.
func (n *Node) codexReplyRemote(ctx context.Context, session string, card *codexCard, r proto.Resolution) error {
	var replies []transcript.CodexReply
	for _, q := range card.ask.Questions {
		if a := strings.TrimSpace(r.Answers[q.Question]); a != "" {
			replies = append(replies, transcript.CodexReply{QuestionItemID: transcript.QuestionItemID(card.ask.CallID, q.Index), Question: q.Question, Answer: a})
		}
	}
	if len(replies) == 0 {
		return errors.New("没有回答任何一道题")
	}
	if err := n.sendCodex(ctx, session, transcript.CodexReplyMessage(replies), nil); err != nil {
		return err
	}
	return n.codexTaken(ctx, session, card, nil)
}
