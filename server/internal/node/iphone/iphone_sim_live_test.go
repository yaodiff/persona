//go:build iphonesimlive

package iphone

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/iphonectl"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The iphone program end to end against a simulator paired with this node (docs/extensions/iphone-bridge.md,
// "Simulator backend"): its status, a look (elements and a screenshot) and a do through the core and the bridge's
// session, with the owner's confirm setting so the input stops for approval and runs only with the approval id.
// Nothing here says anything about a real iPhone.
//
// Run with -tags iphonesimlive, PERSONA_IPHONE_SIM=<udid> and PERSONA_IPHONE_SIM_HOME, the PERSONA_HOME where
// `personad iphone pair <udid>` wrote the record and the bridge is installed (native/iphone-bridge/build.sh
// --sim-runner --install): TestMain clears PERSONA_HOME so no other test reaches the user's.
func TestLiveIPhoneSimulator(t *testing.T) {
	udid, home := os.Getenv("PERSONA_IPHONE_SIM"), os.Getenv("PERSONA_IPHONE_SIM_HOME")
	if udid == "" || home == "" {
		t.Skip("PERSONA_IPHONE_SIM or PERSONA_IPHONE_SIM_HOME not set")
	}
	t.Setenv("PERSONA_HOME", home) // before the node's iphone manager is first made
	ctx := context.Background()
	call := func(c proto.IPhoneCall) *proto.IPhoneResult {
		t.Helper()
		c.Device, c.Session, c.TimeoutMS = udid, "live", 120_000
		raw, _ := json.Marshal(c)
		out, err := Call(ctx, raw)
		if err != nil {
			t.Fatalf("%s: %v", c.Op, err)
		}
		return out
	}

	// The session starts when asked about; wait until the simulator answers.
	deadline := time.Now().Add(3 * time.Minute)
	for {
		s := Status(ctx)
		var d *proto.IPhoneDevice
		for i := range s.Devices {
			if s.Devices[i].ID == udid {
				d = &s.Devices[i]
			}
		}
		if d != nil && d.Ready {
			t.Logf("status: %s %s, %s, ready", d.Name, d.IOS, d.Transport)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not ready: %+v", s)
		}
		<-time.After(2 * time.Second)
	}

	launch := call(proto.IPhoneCall{Op: "do", Args: json.RawMessage(`{"steps":[{"action":"launch","app":"com.apple.Preferences"},{"action":"wait","ms":1500}]}`)})
	t.Logf("launch Settings:\n%s", launch.Text)

	look := call(proto.IPhoneCall{Op: "look", Args: json.RawMessage(`{"screenshot":true}`)})
	t.Logf("look:\n%s", look.Text)
	if look.Image == nil || look.Image.Data == "" {
		t.Fatal("look took no screenshot")
	}
	t.Logf("screenshot: %s %d×%d, %d base64 bytes", look.Image.Mime, look.Image.Width, look.Image.Height, len(look.Image.Data))
	var read iphonectl.LookResult
	if err := json.Unmarshal(look.Output, &read); err != nil || read.Screen == nil || len(read.Screen.Elements) == 0 {
		t.Fatalf("no elements: %s (%v)", look.Output, err)
	}
	field := 0
	for _, e := range read.Screen.Elements {
		if e.Role == "searchField" {
			field = e.N
			break
		}
	}
	if field == 0 {
		t.Fatal("no search field on the Settings screen")
	}

	// With confirm on, the input waits for the owner: nothing is sent until the call comes back approved.
	do := proto.IPhoneCall{Op: "do", Confirm: true, Args: json.RawMessage(`{"numbering":` + jsonInt(read.Screen.Numbering) +
		`,"steps":[{"action":"tap","target":{"n":` + jsonInt(field) + `}},{"action":"type","text":"Wi-Fi"}]}`)}
	asked := call(do)
	summary, _ := json.Marshal(asked.Approval)
	t.Logf("do (confirm on):\nneeds_approval=%v approval_summary=%s\n%s", asked.NeedsApproval, summary, asked.Text)
	if !asked.NeedsApproval || asked.ApprovalID == "" || asked.Approval == nil {
		t.Fatal("the input did not wait for approval")
	}
	// The manager resumes with only the approval id, as it does once the owner allowed it.
	done := call(proto.IPhoneCall{Op: "do", Approved: asked.ApprovalID})
	t.Logf("do (approved):\nneeds_approval=%v\n%s", done.NeedsApproval, done.Text)
	var res iphonectl.DoResult
	if err := json.Unmarshal(done.Output, &res); err != nil || len(res.Steps) != 2 {
		t.Fatalf("do: %s (%v)", done.Output, err)
	}
	for _, s := range res.Steps {
		if s.Delivery != iphonectl.DeliverySent {
			t.Fatalf("step %d %s: %s %s", s.Step, s.What, s.Delivery, s.Detail)
		}
	}
	after := call(proto.IPhoneCall{Op: "look", Args: json.RawMessage(`{"screenshot":false}`)})
	if !strings.Contains(after.Text, "Wi-Fi") {
		t.Fatalf("the typed text is not on the screen:\n%s", after.Text)
	}
	t.Logf("look after:\n%s", after.Text)

	// A newer do on the phone while a step waits for approval replaces it: the old approval id resumes nothing.
	waiting := call(proto.IPhoneCall{Op: "do", Confirm: true, Args: json.RawMessage(`{"steps":[{"action":"key","key":"backspace"}]}`)})
	if !waiting.NeedsApproval || waiting.ApprovalID == "" {
		t.Fatalf("no approval asked: %s", waiting.Text)
	}
	call(proto.IPhoneCall{Op: "do", Args: json.RawMessage(`{"steps":[{"action":"wait","ms":100}]}`)})
	raw, _ := json.Marshal(proto.IPhoneCall{Device: udid, Session: "live", Op: "do", Approved: waiting.ApprovalID, TimeoutMS: 120_000})
	_, err := Call(ctx, raw)
	code, sent := "", ""
	var rerr *rpc.Error
	if errors.As(err, &rerr) {
		var d proto.IPhoneErrorData
		_ = json.Unmarshal(rerr.Data, &d)
		code, sent = rerr.Code, d.Sent
	}
	t.Logf("resume of a replaced approval: code=%s sent=%s: %v", code, sent, err)
	if code != "not_pending" || sent != proto.IPhoneNotSent {
		t.Fatalf("the replaced approval still resumes: %v", err)
	}
	if again := call(proto.IPhoneCall{Op: "look", Args: json.RawMessage(`{"screenshot":false}`)}); !strings.Contains(again.Text, `= "Wi-Fi"`) {
		t.Fatalf("the field changed:\n%s", again.Text)
	}

	// Persona's own app as the hub names it (here Settings stands in for it): no step operates it.
	self := call(proto.IPhoneCall{Op: "do", SelfApps: []string{"com.apple.Preferences"},
		Args: json.RawMessage(`{"steps":[{"action":"key","key":"backspace"}]}`)})
	t.Logf("do in the app named as Persona's own:\n%s", self.Text)
	if err := json.Unmarshal(self.Output, &res); err != nil || len(res.Steps) != 1 || res.Steps[0].Delivery != iphonectl.DeliveryNotSent {
		t.Fatalf("operated the app named as Persona's own: %s", self.Output)
	}
}

func jsonInt(n int) string { b, _ := json.Marshal(n); return string(b) }
