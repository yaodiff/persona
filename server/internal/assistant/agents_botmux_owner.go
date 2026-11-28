package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The BotMux Owner Union ID (Settings › Agents, under "Show botmux sessions"): who the computers' botmux sends speak
// for when botmux takes actor "owner" (docs/integrations/engines.md "botmux"). The default manager keeps it with the
// other Agents display settings (agents_display.go) and hands it to the nodes (node.configure botmux_owner_union_id):
// on a change to every computer online, and on each new connection (nodeConnectCalls). Empty: the nodes send botmux
// the plain external-event trigger. Suggestions come from the computers' bots.json, read-only, and are never saved
// by themselves (GET /api/v1/agents/botmux-owner-candidates).
const agentsBotmuxOwnerKey = "agents.botmux_owner_union_id"

// errBotmuxOwnerUnionID is PUT display-settings' refusal of a value that is not a union_id.
var errBotmuxOwnerUnionID = errors.New("botmux_owner_union_id must be a Feishu union_id (on_…) or empty")

// readBotmuxOwner is the id this manager stores, "" when none is saved.
func (s *Service) readBotmuxOwner(ctx context.Context) (proto.BotmuxOwnerUnionID, error) {
	st := s.store()
	if st == nil {
		return "", errors.New("Agents display settings need a database")
	}
	var raw string
	if _, err := st.Setting(ctx, agentsBotmuxOwnerKey, &raw); err != nil {
		return "", err
	}
	id, _ := proto.ParseBotmuxOwnerUnionID(raw) // only valid ids are saved
	return id, nil
}

// sharedBotmuxOwner is the hub-wide id, read from the default manager when this one is another (as useHerdrTabName).
// ok is false when it cannot be read: a node is then handed nothing, so it keeps what it has.
func (s *Service) sharedBotmuxOwner(ctx context.Context) (proto.BotmuxOwnerUnionID, bool) {
	if s.fleetOn() {
		var shared struct {
			BotmuxOwnerUnionID *string `json:"botmux_owner_union_id"`
		}
		readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := s.appCall(readCtx, assistantwire.AppSettings, assistantwire.AgentsDisplayGet, nil, &shared)
		cancel()
		if err != nil {
			s.logf("botmux owner: read the shared setting: %v", err)
			return "", false
		}
		if shared.BotmuxOwnerUnionID != nil { // another manager's: this one owns none
			id, _ := proto.ParseBotmuxOwnerUnionID(*shared.BotmuxOwnerUnionID)
			return id, true
		}
	}
	id, err := s.readBotmuxOwner(ctx)
	if err != nil {
		s.logf("botmux owner: read the setting: %v", err)
		return "", false
	}
	return id, true
}

// botmuxOwnerNodeCall is a new connection's node.configure with the id (nodeConnectCalls); none when it cannot be read.
func (s *Service) botmuxOwnerNodeCall(ctx context.Context, machine string) (devicewire.NodeCall, bool) {
	id, ok := s.sharedBotmuxOwner(ctx)
	if !ok {
		return devicewire.NodeCall{}, false
	}
	raw, err := json.Marshal(map[string]string{"botmux_owner_union_id": string(id)})
	if err != nil {
		return devicewire.NodeCall{}, false
	}
	return devicewire.NodeCall{MachineID: machine, Method: "node.configure", Params: raw}, true
}

// botmuxOwnerPush serializes the pushes of a change, each sending what is stored when it goes out: of two saves at
// once, the nodes end on the one kept (as devices' setBotmux).
type botmuxOwnerPush struct {
	mu sync.Mutex
}

// syncBotmuxOwnerToNodes hands the stored id to every computer online. A computer offline, or of another manager, gets
// it when it next connects (nodeConnectCalls).
func (s *Service) syncBotmuxOwnerToNodes(ctx context.Context) {
	d := s.devs
	if d == nil {
		return
	}
	for _, m := range d.List() {
		if !m.Online || s.machinePending(ctx, m.ID) {
			continue
		}
		machineID := m.ID
		go func() {
			s.botmuxOwnerPush.mu.Lock()
			defer s.botmuxOwnerPush.mu.Unlock()
			cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			id, err := s.readBotmuxOwner(cctx)
			if err != nil {
				s.logf("[%s] botmux owner configure: %v", machineID, err)
				return
			}
			raw, _ := json.Marshal(map[string]string{"botmux_owner_union_id": string(id)})
			if _, err := d.CallNode(cctx, machineID, "node.configure", raw); err != nil {
				s.logf("[%s] botmux owner configure: %v", machineID, err)
			}
		}()
	}
}

// botmuxOwnerCandidate is one suggestion of GET botmux-owner-candidates: a union_id the computers' bots.json name, how
// many bots (Bots) and of those as their only person (Sole), on how many computers (Machines).
type botmuxOwnerCandidate struct {
	UnionID  proto.BotmuxOwnerUnionID `json:"union_id"`
	Bots     int                      `json:"bots"`
	Sole     int                      `json:"sole"`
	Machines int                      `json:"machines"`
}

type botmuxOwnerCandidates struct {
	Candidates []botmuxOwnerCandidate `json:"candidates"`
}

// botmuxOwnerCandidatesWait bounds the whole fan-out: Settings shows what came back by then.
const botmuxOwnerCandidatesWait = 3 * time.Second

// readBotmuxOwnerCandidates asks every computer online for its bots.json's union_ids (proto.MethodBotmuxOwnerCandidates)
// and merges them, the ids bots name as their only person first. A computer that does not answer in time, or whose node
// predates the method, adds nothing.
func (s *Service) readBotmuxOwnerCandidates(ctx context.Context) botmuxOwnerCandidates {
	out := botmuxOwnerCandidates{Candidates: []botmuxOwnerCandidate{}}
	d := s.devs
	if d == nil {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, botmuxOwnerCandidatesWait)
	defer cancel()
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		got = map[proto.BotmuxOwnerUnionID]*botmuxOwnerCandidate{}
	)
	for _, m := range d.List() {
		if !m.Online || s.machinePending(ctx, m.ID) {
			continue
		}
		machineID := m.ID
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, err := d.CallNode(ctx, machineID, proto.MethodBotmuxOwnerCandidates, nil)
			var res proto.BotmuxOwnerCandidates
			if err != nil || json.Unmarshal(raw, &res) != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, c := range res.Candidates {
				id, ok := proto.ParseBotmuxOwnerUnionID(string(c.UnionID))
				if !ok || id == "" {
					continue // a node says only union_ids; anything else is dropped
				}
				m := got[id]
				if m == nil {
					m = &botmuxOwnerCandidate{UnionID: id}
					got[id] = m
				}
				m.Bots += max(c.Bots, 0)
				m.Sole += max(c.Sole, 0)
				m.Machines++
			}
		}()
	}
	wg.Wait()
	for _, c := range got {
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
		if a.Machines != b.Machines {
			return a.Machines > b.Machines
		}
		return a.UnionID < b.UnionID
	})
	if len(out.Candidates) > botmuxOwnerCandidatesMax {
		out.Candidates = out.Candidates[:botmuxOwnerCandidatesMax]
	}
	return out
}

// botmuxOwnerCandidatesMax: Settings offers the first; a long list helps nobody pick.
const botmuxOwnerCandidatesMax = 8

func (s *Service) botmuxOwnerRoutes() {
	// Full access only: the suggestions are ids of the people the bots answer, and only full access may save one.
	s.handle("GET /api/v1/agents/botmux-owner-candidates", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("only full access may read BotMux owner suggestions"))
			return
		}
		apierr.WriteJSON(w, s.readBotmuxOwnerCandidates(r.Context()))
	})
}
