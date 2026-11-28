//go:build persona_core_only || persona_without_github

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/githubwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func withoutModuleCIWatch(t *testing.T, s *Service, f *fakeHub, c proto.Session) db.SessionWork {
	t.Helper()
	f.sessions["box"] = []proto.Session{c}
	fallback := turnEnd.Add(10 * time.Minute)
	e := db.SessionWork{MachineID: "box", SessionID: c.SessionID, TurnAt: turnEnd, WakeAt: &fallback,
		Work: proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, Wait: "CI", WakeAt: &fallback}}
	startCIWatch(c, &e, turnEnd, "", "")
	if e.Work.CI == nil {
		t.Fatal("CI watch was not registered")
	}
	s.putWork(t.Context(), e)
	return e
}

func TestCIWithoutGitHubModuleConsumesInstalledSnapshot(t *testing.T) {
	s, f := workService(t)
	c := stopped("ci", "Waiting for https://github.com/example/repo/pull/12")
	withoutModuleCIWatch(t, s, f, c)
	const head = "0123456789abcdef0123456789abcdef01234567"
	pending := true
	connectorCalls := 0
	s.ext = fakeCIExtensions{call: func(_ context.Context, repo string, number int) (githubwire.PRCheckSnapshot, time.Time, bool, error) {
		connectorCalls++
		if repo != "example/repo" || number != 12 {
			t.Fatalf("wrong target %s #%d", repo, number)
		}
		state := "success"
		if pending {
			state = "pending"
		}
		snapshot := githubwire.PRCheckSnapshot{Head: head}
		snapshot.AddRun(1, "lint", "completed", "failure")
		snapshot.AddRun(2, "test", "completed", "success")
		snapshot.AddStatus("preview", state)
		snapshot.Finish()
		return snapshot, time.Time{}, true, nil
	}}
	nodeCalls := 0
	f.answerNode("git.pr", func(_ string, _ json.RawMessage) (any, error) {
		nodeCalls++
		return nil, errors.New("the connector snapshot must not fall back to the node")
	})
	var sends []string
	f.answerNode(DeviceSessionSendOnce, func(_ string, raw json.RawMessage) (any, error) {
		var args struct{ Text string }
		_ = json.Unmarshal(raw, &args)
		sends = append(sends, args.Text)
		return map[string]any{}, nil
	})

	s.workWake(t.Context(), s.newWorkView(t.Context()), [2]string{"box", "ci"}, turnEnd.Add(time.Minute))
	got := entryOf(t, s, "ci")
	if len(sends) != 0 || got.Work.CI == nil || got.Work.CI.Observed != "" || got.Work.CI.Polls != 1 {
		t.Fatalf("pending snapshot: sends=%v work=%+v", sends, got.Work.CI)
	}
	pending = false
	s.workWake(t.Context(), s.newWorkView(t.Context()), [2]string{"box", "ci"}, *got.WakeAt)
	got = entryOf(t, s, "ci")
	if len(sends) != 0 || got.Work.CI == nil || got.Work.CI.Observed == "" || got.Work.CI.ObservedAt.IsZero() {
		t.Fatalf("first complete snapshot: sends=%v work=%+v", sends, got.Work.CI)
	}
	s.workWake(t.Context(), s.newWorkView(t.Context()), [2]string{"box", "ci"}, got.Work.CI.ObservedAt.Add(2*time.Minute))
	if connectorCalls != 3 || nodeCalls != 0 || len(sends) != 1 || !strings.Contains(sends[0], "lint") {
		t.Fatalf("connector=%d node=%d sends=%v", connectorCalls, nodeCalls, sends)
	}
	if got := entryOf(t, s, "ci"); got.Work.CI != nil || got.WakeAt != nil {
		t.Fatalf("completed watch stayed armed: %+v", got)
	}
}

func TestCIWithoutGitHubModuleUnavailableAndGuestDoNotUseOwnerCredentials(t *testing.T) {
	t.Run("unavailable without a node fallback", func(t *testing.T) {
		s, f := workService(t)
		c := stopped("ci", "CI https://github.com/example/repo/pull/12")
		c.Cwd = ""
		e := withoutModuleCIWatch(t, s, f, c)
		fallback := e.Work.CI.FallbackAt
		connectorCalls := 0
		s.ext = fakeCIExtensions{call: func(context.Context, string, int) (githubwire.PRCheckSnapshot, time.Time, bool, error) {
			connectorCalls++
			return githubwire.PRCheckSnapshot{}, time.Time{}, false, nil
		}}
		sends := 0
		f.answerNode(DeviceSessionSendOnce, func(_ string, _ json.RawMessage) (any, error) { sends++; return map[string]any{}, nil })
		s.workWake(t.Context(), s.newWorkView(t.Context()), [2]string{"box", "ci"}, turnEnd.Add(time.Minute))
		got := entryOf(t, s, "ci")
		if connectorCalls != 1 || sends != 0 || got.Work.CI != nil || got.WakeAt == nil || !got.WakeAt.Equal(fallback) {
			t.Fatalf("connector=%d sends=%d work=%+v", connectorCalls, sends, got)
		}
	})

	t.Run("guest", func(t *testing.T) {
		s, f := workService(t)
		c := stopped("ci", "CI https://github.com/example/repo/pull/12")
		if err := s.guestSet(t.Context(), "box", "ci", []string{"another_tool"}); err != nil {
			t.Fatal(err)
		}
		connectorCalls, nodeCalls := 0, 0
		s.ext = fakeCIExtensions{call: func(context.Context, string, int) (githubwire.PRCheckSnapshot, time.Time, bool, error) {
			connectorCalls++
			return githubwire.PRCheckSnapshot{Count: 1, Fingerprint: "owner", Failures: []string{"private-check"}}, time.Time{}, true, nil
		}}
		f.answerNode("git.pr", func(_ string, _ json.RawMessage) (any, error) { nodeCalls++; return nil, nil })
		e := db.SessionWork{MachineID: "box", SessionID: "ci", Work: proto.WorkState{CI: &proto.WorkCI{Repo: "example/repo", Number: 12}}}
		result, reset, available := s.ciChecks(t.Context(), e, c)
		if available || !reset.IsZero() || result.count != 0 || result.fingerprint != "" || len(result.failures) != 0 || connectorCalls != 0 || nodeCalls != 0 {
			t.Fatalf("result=%+v reset=%v available=%v connector=%d node=%d", result, reset, available, connectorCalls, nodeCalls)
		}
	})
}

func TestCIWithoutGitHubModuleEmptySnapshotIsNotCompletion(t *testing.T) {
	s, f := workService(t)
	c := stopped("ci", "CI https://github.com/example/repo/pull/12")
	c.Cwd = ""
	e := withoutModuleCIWatch(t, s, f, c)
	fallback := e.Work.CI.FallbackAt
	connectorCalls := 0
	s.ext = fakeCIExtensions{call: func(context.Context, string, int) (githubwire.PRCheckSnapshot, time.Time, bool, error) {
		connectorCalls++
		snapshot := githubwire.PRCheckSnapshot{Head: "0123456789abcdef0123456789abcdef01234567"}
		snapshot.Finish()
		return snapshot, time.Time{}, true, nil
	}}
	sends := 0
	f.answerNode(DeviceSessionSendOnce, func(_ string, _ json.RawMessage) (any, error) { sends++; return map[string]any{}, nil })

	s.workWake(t.Context(), s.newWorkView(t.Context()), [2]string{"box", "ci"}, turnEnd.Add(time.Minute))
	got := entryOf(t, s, "ci")
	if sends != 0 || got.Work.CI == nil || got.Work.CI.Observed != "" {
		t.Fatalf("empty snapshot completed early: sends=%d work=%+v", sends, got.Work.CI)
	}
	s.workWake(t.Context(), s.newWorkView(t.Context()), [2]string{"box", "ci"}, fallback)
	got = entryOf(t, s, "ci")
	if connectorCalls != 2 || sends != 0 || got.Work.CI != nil || got.WakeAt == nil || !got.WakeAt.Equal(fallback) {
		t.Fatalf("scheduled fallback: connector=%d sends=%d work=%+v", connectorCalls, sends, got)
	}
}

func TestCIWithoutGitHubModuleNodeFallbackKeepsScheduledWake(t *testing.T) {
	s, f := workService(t)
	c := stopped("ci", "CI https://github.com/example/repo/pull/12")
	withoutModuleCIWatch(t, s, f, c)
	connectorCalls, nodeCalls := 0, 0
	s.ext = fakeCIExtensions{call: func(context.Context, string, int) (githubwire.PRCheckSnapshot, time.Time, bool, error) {
		connectorCalls++
		return githubwire.PRCheckSnapshot{}, time.Time{}, false, nil
	}}
	f.answerNode("git.pr", func(_ string, _ json.RawMessage) (any, error) {
		nodeCalls++
		return map[string]any{"pr": map[string]any{
			"number": 12, "url": "https://github.com/example/repo/pull/12", "head_oid": "0123456789abcdef0123456789abcdef01234567",
			"checks_complete": true, "checks": []map[string]string{{"name": "node-lint", "state": "fail"}},
		}}, nil
	})
	var sends []string
	f.answerNode(DeviceSessionSendOnce, func(_ string, raw json.RawMessage) (any, error) {
		var args struct{ Text string }
		_ = json.Unmarshal(raw, &args)
		sends = append(sends, args.Text)
		return map[string]any{}, nil
	})

	s.workWake(t.Context(), s.newWorkView(t.Context()), [2]string{"box", "ci"}, turnEnd.Add(time.Minute))
	got := entryOf(t, s, "ci")
	if len(sends) != 0 || got.Work.CI == nil || got.Work.CI.Observed == "" || got.WakeAt == nil {
		t.Fatalf("node result was not held for stability: sends=%v work=%+v", sends, got)
	}
	s.workWake(t.Context(), s.newWorkView(t.Context()), [2]string{"box", "ci"}, *got.WakeAt)
	if connectorCalls != 2 || nodeCalls != 2 || len(sends) != 1 || !strings.Contains(sends[0], "node-lint") {
		t.Fatalf("connector=%d node=%d sends=%v", connectorCalls, nodeCalls, sends)
	}
	if got := entryOf(t, s, "ci"); got.Work.CI != nil || got.WakeAt != nil {
		t.Fatalf("node-backed watch stayed armed: %+v", got)
	}
}
