package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/proto"
)

const doneCloseout = "改好了。\n\n<closeout status=\"done\" merged=\"yes\" deployed=\"no\">\n" +
	"did: 重试改成退避，不再原地循环\nartifact: pr https://github.com/example/app/pull/12\nartifact: branch fix/12-retry\n" +
	"verified: go test ./retry，14 个通过\n</closeout>"

// A turn whose closeout says done is done by rule: the triage model is not asked.
func TestCloseoutDoneNeedsNoModel(t *testing.T) {
	s, f := workService(t)
	f.triage = `{"phase":"blocked","on":"user","line":"不该问到这里"}`
	f.setSessions("box", stopped("s1", doneCloseout))
	triage(t, s, "s1")
	w, ok := reportOf(t, s, "s1")
	if !ok || w.Phase != proto.WorkDone || w.By != "rule" || w.Line != "重试改成退避，不再原地循环" {
		t.Fatalf("work %+v", w)
	}
	if c := w.Closeout; c == nil || c.Merged != proto.CloseoutYes || c.Deployed != proto.CloseoutNo || c.Verified == "" {
		t.Fatalf("closeout %+v", w.Closeout)
	}
	if len(w.Artifacts) != 2 || w.Artifacts[0].Kind != "pr" {
		t.Fatalf("artifacts %+v", w.Artifacts)
	}
	if len(f.triageAsked) != 0 {
		t.Fatalf("the model was asked %d times", len(f.triageAsked))
	}
}

func TestTriageUnfinishedCloseoutSkipsModel(t *testing.T) {
	for _, tc := range []struct {
		name, report, phase, on, wait string
	}{
		{"legacy blocked wait", "<closeout status=\"blocked\">\nwaiting: Review\n</closeout>", proto.WorkBlocked, proto.WaitExternal, "Review"},
		{"unfinished", "<closeout status=\"not_done\">\nmissing: Live verification\n</closeout>", "", "", ""},
		{"owner without a request", "<closeout status=\"blocked\">\nunblock: user\n</closeout>", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := workService(t)
			setTriageAnswer(f, `{"phase":"done","line":"Everything is complete"}`)
			f.setSessions("box", stopped("s1", "Updates so far.\n"+tc.report))
			triage(t, s, "s1")
			if n := len(triageCalls(f)); n != 0 {
				t.Fatalf("closeout triggered %d model calls", n)
			}
			w, shown := reportOf(t, s, "s1")
			if !shown || w.Closeout == nil || w.By != "rule" || w.Line == "" || w.Phase != tc.phase || w.On != tc.on || w.Wait != tc.wait {
				t.Fatalf("report = %+v (shown %v), want phase %q on %q waiting for %q", w, shown, tc.phase, tc.on, tc.wait)
			}
			if w.WakeAt != nil || len(s.workCards()) != 0 {
				t.Fatalf("closeout scheduled an action: %+v", w)
			}
		})
	}
}

// The stop is held only under the main switch and the closeout switch, and never for an assistant conversation.
func TestCloseoutCheck(t *testing.T) {
	s, f := workService(t)
	ctx := context.Background()
	chat := stopped("chat", "x")
	chat.Assistant = true
	f.setSessions("box", stopped("s1", "x"), chat)
	ask := func(sid string) string {
		t.Helper()
		return s.closeoutCheck(ctx, "box", assistantwire.CloseoutCheckRequest{SessionID: sid}).Reason
	}
	if r := ask("s1"); r != "" {
		t.Fatalf("held with the switch off: %q", r)
	}
	if err := s.putTriageSwitches(ctx, TriageSwitches{Closeout: true}); err != nil {
		t.Fatal(err)
	}
	if r := ask("s1"); r != "" {
		t.Fatalf("held with the triage off: %q", r)
	}
	if err := s.putTriageSwitches(ctx, TriageSwitches{Enabled: true, Closeout: true}); err != nil {
		t.Fatal(err)
	}
	if r := ask("s1"); !strings.Contains(r, `<closeout status="done|not_done|blocked"`) {
		t.Fatalf("no form in the reason: %q", r)
	}
	if r := ask("chat"); r != "" {
		t.Fatalf("an assistant conversation was held: %q", r)
	}
	if r := ask(""); r != "" {
		t.Fatalf("held without a session: %q", r)
	}
}

// An app that does not know the switch leaves it as it was.
func TestCloseoutSwitchSurvivesOlderApps(t *testing.T) {
	s, _ := workService(t)
	if w := serve(s, "PUT", "/api/v1/agents/triage", `{"enabled":true,"closeout":true}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"closeout":true`) {
		t.Fatalf("PUT %d %s", w.Code, w.Body)
	}
	if w := serve(s, "PUT", "/api/v1/agents/triage", `{"enabled":true,"wake":true}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"closeout":true`) {
		t.Fatalf("an older app's PUT cleared it: %d %s", w.Code, w.Body)
	}
	if w := serve(s, "PUT", "/api/v1/agents/triage", `{"enabled":true,"closeout":false}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"closeout":false`) {
		t.Fatalf("PUT off %d %s", w.Code, w.Body)
	}
}

func TestGoldenAgentCloseoutRequired(t *testing.T) {
	golden(t, "agent_closeout_required", renderCloseoutRequired("简体中文", closeout.Example))
	golden(t, "agent_closeout_required.no_language", renderCloseoutRequired("", closeout.Example))
}

const mergedCloseout = "合了。\n<closeout status=\"done\" merged=\"yes\" deployed=\"yes\">\ndid: 修了重试\nartifact: pr https://github.com/o/r/pull/9\n</closeout>"

// The hub looks at the closeout's pull request itself: what it finds is kept beside the claim, and a claim it
// disproves is not a finished turn.
func TestCloseoutIsChecked(t *testing.T) {
	s, f, _, hub, forge := deployService(t)
	s.work.switches = &TriageSwitches{Enabled: true}
	f.sessions["box"] = nil

	hub.deploy(newHub) // the running hub contains the merge
	f.setSessions("box", stopped("s1", mergedCloseout))
	triage(t, s, "s1")
	if w := entryOf(t, s, "s1").Work; w.Phase != proto.WorkDone || w.Closeout == nil || w.Closeout.Checked == nil ||
		*w.Closeout.Checked != (proto.CloseoutChecked{Merged: proto.CloseoutYes, Deployed: proto.CloseoutYes}) {
		t.Fatalf("confirmed: %+v %+v", w, w.Closeout)
	}

	hub.deploy(oldHub) // merged, and the running hub does not contain it: "deployed: yes" is not so
	f.setSessions("box", stopped("s2", mergedCloseout))
	triage(t, s, "s2")
	if w := entryOf(t, s, "s2").Work; w.Phase != "" || w.Closeout == nil || w.Closeout.Checked == nil || w.Closeout.Checked.Deployed != proto.CloseoutNo {
		t.Fatalf("a disproved deploy read as %+v %+v", w, w.Closeout)
	}

	forge.merged = false // not merged at all
	s.deploy.cache = nil
	f.setSessions("box", stopped("s3", mergedCloseout))
	triage(t, s, "s3")
	if w := entryOf(t, s, "s3").Work; w.Phase != "" || w.Closeout.Checked == nil || w.Closeout.Checked.Merged != proto.CloseoutNo {
		t.Fatalf("a disproved merge read as %+v %+v", w, w.Closeout)
	}
	if len(f.triageAsked) != 0 {
		t.Fatalf("the model was asked %d times", len(f.triageAsked))
	}
}

func TestCloseoutDelivery(t *testing.T) {
	for name, tc := range map[string]struct {
		reply string
		state string
		ok    bool
	}{
		"live":          {"<closeout status=\"done\" merged=\"yes\" deployed=\"yes\">\n</closeout>", deliveryLive, true},
		"merged":        {"<closeout status=\"done\" merged=\"yes\" deployed=\"no\">\n</closeout>", deliveryMerged, true},
		"at a pr":       {"<closeout status=\"blocked\" merged=\"no\">\nartifact: pr #12\nwaiting: review\n</closeout>", deliveryPR, true},
		"nothing ships": {"<closeout status=\"done\" merged=\"n/a\" deployed=\"n/a\">\n</closeout>", deliveryNone, true},
		"not done":      {"<closeout status=\"not_done\">\n</closeout>", "", false},
		"its own line":  {"<closeout status=\"done\" merged=\"yes\" deployed=\"yes\">\n</closeout>\n交付：停在 PR；卡点：等 review；谁来解：用户", deliveryPR, true},
	} {
		if d, ok := deliveryMark(tc.reply); ok != tc.ok || d.State != tc.state {
			t.Errorf("%s: delivery %+v ok=%v, want %q %v", name, d, ok, tc.state, tc.ok)
		}
	}
}
