package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func (s *Service) agyRelayText(ctx context.Context, machine, to string, msg relayMsg) string {
	return s.agyRelayBlock(ctx, machine, to, msg) + relayDeliveryText(msg)
}

func TestAgyUnlinkedRelayUsesFinalRecipientAndCurrentInventory(t *testing.T) {
	for _, kind := range []string{sigUnjudged, sigNeedsUserExternalInfo} {
		t.Run(kind, func(t *testing.T) {
			s, f, todo, pane := unlinkedPaneFixture(t, "en")
			ctx := context.Background()
			f.setSessions("server", proto.Session{SessionID: "conv1", Assistant: true},
				proto.Session{SessionID: "health-lead", Assistant: true, Space: "health"})
			f.mu.Lock()
			f.known["health-lead"] = true
			f.mu.Unlock()
			candidate := proto.Session{SessionID: "agy-before-transfer", Agent: "agy", SourceKind: proto.SourceRuntime,
				SourceInstanceID: "agy", Cwd: pane.Cwd, UpdatedAt: time.Now()}
			f.setSessions("box", pane, candidate)
			check, err := s.judgeTaskDone(ctx, todo)
			if err != nil || check.reasonKey != testUnlinkedReason {
				t.Fatalf("judge: %+v, %v", check, err)
			}
			if kind == sigNeedsUserExternalInfo {
				todo.Supervision = todo.Supervision.clone()
				todo.Supervision.State = reviewNeedsUser
				pane.MachineID = "box"
				s.takeTurn(ctx, todo, pane, nil, turnClose{}, false, todo.CreatedAt, turnDone)
			}
			putTodo(s, todo)
			s.superviseJudged(ctx, todo, todo.CreatedAt, check, nil)
			s.supervise.mu.Lock()
			var signal superviseSignal
			for _, g := range s.supervise.pending {
				if g.Kind == kind {
					signal = g
				}
			}
			s.supervise.mu.Unlock()
			if len(signal.Unlinked) != 1 || signal.Detail != check.Reason {
				t.Fatalf("pending signal has no safe short diagnostic: %+v", signal)
			}
			entry, ok := s.digestEntry(ctx, signal, "")
			if !ok {
				t.Fatal("the unlinked signal is missing from the digest")
			}
			body := s.renderQueuedDigest(ctx, "conv1", []prompts.SuperviseEntry{entry})
			msg := relayMsg{Tool: superviseTool, Text: body, Signals: encodeDigestSignals([]superviseSignal{signal})}
			if strings.Contains(msg.Text+msg.Signals, candidate.SessionID) {
				t.Fatalf("durable relay contains a candidate: %+v", msg)
			}
			if _, _, err := s.sendRelay(ctx, "server", "conv1", msg); err != nil {
				t.Fatal(err)
			}
			sent := f.noticesTo("conv1")
			if len(sent) == 0 {
				t.Fatal("no relay reached the original supervisor")
			}
			rendered := sent[len(sent)-1]
			if !strings.Contains(rendered, candidate.SessionID) || relayWords(rendered) != relayWords(body) {
				t.Fatalf("original recipient didn't get a confirmable candidate detail: %s", rendered)
			}
			f.setSessions("box", pane)
			if got := s.agyRelayText(ctx, "server", "conv1", msg); strings.Contains(got, candidate.SessionID) {
				t.Fatalf("redelivery kept the old candidate: %s", got)
			}
			f.setSessions("box", pane, candidate)
			todo.Supervision = todo.Supervision.clone()
			todo.Supervision.Supervisor = "health-lead"
			todo.Supervision.SupervisorSource = "explicit"
			putTodo(s, todo)
			if got := s.agyRelayText(ctx, "server", "conv1", msg); got != body {
				t.Fatalf("a previous supervisor retained access after transfer: %s", got)
			}
			if n := s.queueRelay("conv1", msg); n != 1 {
				t.Fatalf("queue depth %d", n)
			}
			s.relays.mu.Lock()
			head := s.relays.st.Queue["conv1"][0]
			s.relays.mu.Unlock()
			_, stale, keep := s.recheckQueuedDigest(ctx, "conv1", head)
			if !stale || keep {
				t.Fatal("the queued signal did not move away from its previous recipient")
			}
			s.relays.mu.Lock()
			moved := s.relays.st.Queue["health-lead"]
			s.relays.mu.Unlock()
			if len(moved) != 1 || strings.Contains(moved[0].Text+moved[0].Signals, candidate.SessionID) {
				t.Fatalf("unexpected retargeted queue: %+v", moved)
			}
			if _, _, err := s.sendRelay(ctx, "server", "health-lead", moved[0]); err != nil {
				t.Fatal(err)
			}
			redirected := f.noticesTo("health-lead")
			if len(redirected) != 1 || !strings.Contains(redirected[0], check.Reason) || strings.Contains(redirected[0], candidate.SessionID) {
				t.Fatalf("wrong diagnostic after transfer to another space: %q", redirected)
			}
		})
	}
}

func TestAgyUnlinkedTaskLeaderReceivesOnlyShortReason(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	startTaskLeader(t, s, f)
	worker := planWorker(t, s, f, nil)
	link := worker.Agents[0]
	pane := proto.Session{SessionID: link.SessionID, MachineID: link.MachineID, Cwd: "/code/app", Agent: "agy", PaneID: "w2:p1",
		Status: proto.StatusDone, UpdatedAt: time.Now(), LastText: "stale preview from an earlier binding"}
	candidate := proto.Session{SessionID: "agy-conversation", Agent: "agy", SourceKind: proto.SourceRuntime, SourceInstanceID: "agy",
		Cwd: pane.Cwd, UpdatedAt: time.Now()}
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Agent: "claude", Status: proto.StatusIdle}, pane, candidate)
	f.setItems(proto.Item{ID: "agy#0", Role: "system", Kind: proto.KindNotice})
	check, err := s.judgeTaskDone(ctx, worker)
	if err != nil || check.reasonKey != testUnlinkedReason {
		t.Fatalf("expected an unlinked pane: %+v, %v", check, err)
	}
	s.superviseJudged(ctx, worker, time.Now(), check, nil)
	waitFor(t, "the short diagnostic to reach the task leader", func() bool {
		for _, text := range sendsTo(f, "box", "s1") {
			if strings.Contains(text, check.Reason) {
				if strings.Contains(text, candidate.SessionID) || strings.Contains(text, pane.LastText) {
					t.Errorf("task leader received a candidate or an unverified reply preview: %s", text)
				}
				return true
			}
		}
		return false
	})
}
