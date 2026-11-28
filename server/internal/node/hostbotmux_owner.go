package node

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// The BotMux Owner Union ID (Settings › Agents, docs/integrations/engines.md "botmux"): the hub hands it with
// node.configure botmux_owner_union_id, and the botmux host sends as actor "owner" only with one (botmuxHost.Prompt).
// The node keeps it in memory: every new connection hands it again.

// setBotmuxOwner keeps the id the hub handed and gives it to the botmux host. Anything that is not a union_id clears it:
// a send never speaks for an id the node cannot vouch for.
func (n *Node) setBotmuxOwner(raw string) {
	id, ok := proto.ParseBotmuxOwnerUnionID(raw)
	if !ok {
		n.logf("botmux: ignoring a BotMux Owner Union ID that is not a union_id")
	}
	n.eng.mu.Lock()
	n.eng.botmuxOwner = id
	n.syncBotmuxOwner()
	n.eng.mu.Unlock()
}

// syncBotmuxOwner gives the botmux host the id kept (after the hosts are replaced, as syncHerdrHostBlocked). n.eng.mu
// is held.
func (n *Node) syncBotmuxOwner() {
	if n.hosts == nil {
		return
	}
	if h, ok := n.hosts.Get(host.Botmux).(*botmuxHost); ok {
		h.setOwnerUnionID(n.eng.botmuxOwner)
	}
}

// Settings' suggestions for the BotMux Owner Union ID, read-only (proto.MethodBotmuxOwnerCandidates).
func init() { nodeRPC(proto.MethodBotmuxOwnerCandidates, (*Node).botmuxOwnerSuggestions) }

// botmuxOwnerSuggestions answers botmux.owner_candidates: what this computer's bots.json names, none without botmux.
func (n *Node) botmuxOwnerSuggestions(context.Context, struct{}) (proto.BotmuxOwnerCandidates, error) {
	if botmuxBin() == "" {
		return proto.BotmuxOwnerCandidates{Candidates: []proto.BotmuxOwnerCandidate{}}, nil
	}
	return botmuxOwnerCandidates(botmuxBotsPath()), nil
}

// botmuxBotsMaxBytes bounds the bots.json read for candidates: a registry of hundreds of bots is far below it.
const botmuxBotsMaxBytes = 8 << 20

// botmuxBotsPath is the bots.json botmux loads, as botmux resolves it: BOTS_CONFIG (the exact file), else
// ~/.botmux/bots.json. "" when neither can be named.
func botmuxBotsPath() string {
	if p := os.Getenv("BOTS_CONFIG"); p != "" {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".botmux", "bots.json")
}

// botmuxOwnerBot is the only part of a bots.json entry read: every other field (secrets, tokens, e-mail addresses,
// paths) is never decoded.
type botmuxOwnerBot struct {
	OwnerUnionID string            `json:"ownerUnionId"`
	AllowedUsers []json.RawMessage `json:"allowedUsers"`
}

// botmuxOwnerCandidates reads bots.json at path (read-only) for the union_ids it names: each bot's ownerUnionId and the
// "on_…" entries of its allowedUsers, deduplicated, with how many bots name each and how many as their only person.
// Nothing is written or kept, and the answer carries only union_ids. A missing or unreadable file is no candidates.
func botmuxOwnerCandidates(path string) proto.BotmuxOwnerCandidates {
	out := proto.BotmuxOwnerCandidates{Candidates: []proto.BotmuxOwnerCandidate{}}
	if path == "" {
		return out
	}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, botmuxBotsMaxBytes+1))
	if err != nil || len(b) > botmuxBotsMaxBytes {
		return out
	}
	var bots []botmuxOwnerBot
	if json.Unmarshal(b, &bots) != nil {
		var wrapped struct {
			Bots []botmuxOwnerBot `json:"bots"`
		}
		if json.Unmarshal(b, &wrapped) != nil {
			return out
		}
		bots = wrapped.Bots
	}
	by := map[proto.BotmuxOwnerUnionID]*proto.BotmuxOwnerCandidate{}
	for _, bot := range bots {
		named := map[proto.BotmuxOwnerUnionID]bool{}
		owner, _ := proto.ParseBotmuxOwnerUnionID(bot.OwnerUnionID)
		if owner != "" {
			named[owner] = true
		}
		var allowed []proto.BotmuxOwnerUnionID
		people := 0
		for _, raw := range bot.AllowedUsers {
			var entry string
			if json.Unmarshal(raw, &entry) != nil || entry == "" {
				continue
			}
			people++
			if id, ok := proto.ParseBotmuxOwnerUnionID(entry); ok && id != "" {
				allowed = append(allowed, id)
				named[id] = true
			}
		}
		// The person botmux takes as the owner without more configuration: the ownerUnionId, else the only person of
		// allowedUsers.
		sole := owner
		if sole == "" && people == 1 && len(allowed) == 1 {
			sole = allowed[0]
		}
		for id := range named {
			c := by[id]
			if c == nil {
				c = &proto.BotmuxOwnerCandidate{UnionID: id}
				by[id] = c
			}
			c.Bots++
			if id == sole {
				c.Sole++
			}
		}
	}
	for _, c := range by {
		out.Candidates = append(out.Candidates, *c)
	}
	sort.Slice(out.Candidates, func(i, j int) bool {
		a, b := out.Candidates[i], out.Candidates[j]
		if a.Sole != b.Sole {
			return a.Sole > b.Sole
		}
		if a.Bots != b.Bots {
			return a.Bots > b.Bots
		}
		return a.UnionID < b.UnionID
	})
	if len(out.Candidates) > botmuxOwnerCandidatesMax {
		out.Candidates = out.Candidates[:botmuxOwnerCandidatesMax]
	}
	return out
}

// botmuxOwnerCandidatesMax: the first candidates are the ones offered; the rest are no help in Settings.
const botmuxOwnerCandidatesMax = 8
