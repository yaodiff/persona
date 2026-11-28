package extwire

import (
	"encoding/json"
	"testing"
)

func TestAuthorizedOwnerWire(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reply     string
		verdict   OwnerVerdict
		roundtrip string
	}{
		{"legacy", `{"ok":true}`, OwnerUnreported, `{"ok":true}`},
		{"third party", `{"ok":true,"owner":false}`, OwnerThirdParty, `{"ok":true,"owner":false}`},
		{"owner", `{"ok":true,"owner":true}`, OwnerConfirmed, `{"ok":true,"owner":true}`},
		{"null", `{"ok":true,"owner":null}`, OwnerUnreported, `{"ok":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reply Authorized
			if err := json.Unmarshal([]byte(tc.reply), &reply); err != nil || reply.Owner != tc.verdict {
				t.Fatalf("decode %s: owner %v, want %v (%v)", tc.reply, reply.Owner, tc.verdict, err)
			}
			encoded, err := json.Marshal(reply)
			if err != nil || string(encoded) != tc.roundtrip {
				t.Errorf("encode: %s, want %s (%v)", encoded, tc.roundtrip, err)
			}
		})
	}
	var reply Authorized
	if err := json.Unmarshal([]byte(`{"owner":"true"}`), &reply); err == nil {
		t.Fatal("decoded a string as an owner verdict")
	}
}
