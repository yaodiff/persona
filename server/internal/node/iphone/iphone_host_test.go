package iphone

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/iphone"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The session's errors reach the manager with their code, and with whether the operation was sent when the session
// can tell; a bridge's own answer is left to its code.
func TestIPhoneSessionErrors(t *testing.T) {
	for _, c := range []struct {
		err  error
		code string
		sent string
	}{
		{&iphone.Error{Code: iphone.CodeNotPaired, Message: "no record"}, "not_paired", proto.IPhoneNotSent},
		{&iphone.Error{Code: iphone.CodeUnreachable}, "unreachable", proto.IPhoneNotSent},
		{&iphone.Error{Code: iphone.CodeUncertain}, "uncertain", proto.IPhoneMaybeSent},
		{errors.New("plain"), "", proto.IPhoneMaybeSent},
		{&iphone.Error{Code: "locked"}, "locked", ""},
	} {
		var e *rpc.Error
		if !errors.As(iphoneSessionErr(c.err), &e) || e.Code != c.code {
			t.Fatalf("%v: %+v", c.err, e)
		}
		var d proto.IPhoneErrorData
		_ = json.Unmarshal(e.Data, &d)
		if d.Sent != c.sent {
			t.Errorf("%v: sent %q", c.err, d.Sent)
		}
	}
	// A bridge's refusal is not sent by its code, through the call's classification.
	err := iphoneStepErr(context.Background(), context.Background(), "do", iphoneSessionErr(&iphone.Error{Code: "locked"}), iphoneCallDefault, false)
	if code, sent := iphoneCallErr(t, err); code != "locked" || sent != proto.IPhoneNotSent {
		t.Fatalf("locked: %s %s", code, sent)
	}
}

// Through a real iphone.Manager with a stand-in bridge: its version, a phone on USB, and no pairing record.
func TestIPhoneSessionsWithAStandInBridge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for the bridge")
	}
	bridge := filepath.Join(t.TempDir(), "persona-iphone-bridge")
	script := `#!/bin/sh
case "$1" in
version) echo "persona-iphone-bridge 0.1.0 (wire 1)" ;;
usb-list) echo '[{"udid":"00008110-000A1B2C3D4E5F60","name":"Phone","product_version":"18.1","model":"iPhone16,2"}]' ;;
*) exit 1 ;;
esac
`
	if err := os.WriteFile(bridge, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(iphone.BridgeEnv, bridge)
	m := iphone.New(t.TempDir())
	t.Cleanup(m.Close)
	useIPhones(t, nil, nil)
	iphones = iphoneSessions{func() *iphone.Manager { return m }}
	iphoneBridgeSeen.at = time.Time{}
	t.Cleanup(func() { iphoneBridgeSeen.at = time.Time{} })

	p := Status(context.Background())
	if !p.Installed || p.Version != "0.1.0" || p.Note != iphoneNoteNonePaired || len(p.Devices) != 1 {
		t.Fatalf("status %+v", p)
	}
	if d := p.Devices[0]; d.ID != "00008110-000A1B2C3D4E5F60" || !d.USB || d.Paired || d.ProductType != "iPhone16,2" || d.IOS != "18.1" || d.Note != iphoneNoteUSB {
		t.Fatalf("the phone on USB %+v", d)
	}
	_, err := Call(context.Background(), json.RawMessage(`{"op":"look"}`))
	if code, sent := iphoneCallErr(t, err); code != "not_paired" || sent != proto.IPhoneNotSent {
		t.Fatalf("nothing paired: %s %s", code, sent)
	}
}
