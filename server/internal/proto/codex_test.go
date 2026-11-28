package proto

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSessionCapabilitiesTakeoverAndRewindRoundTrip(t *testing.T) {
	for _, raw := range []string{
		`{"session_id":"s","capabilities":{"takeover":{"running":true,"can_resume":true},"rewind":true,"rewind_code":false}}`,
		`{"session_id":"s","capabilities":{"takeover":{"running":false,"can_resume":true,"app_owned":true}}}`,
		`{"session_id":"s","capabilities":{"takeover":{"running":false,"can_resume":false,"reason":"app_running","app_owned":true}}}`,
		`{"session_id":"s","capabilities":{"takeover":{"running":false,"can_resume":false,"reason":"encrypted_context"}}}`,
		`{"session_id":"s","capabilities":{"rewind":false,"rewind_code":true}}`,
		`{"session_id":"s","capabilities":{}}`,
	} {
		var session Session
		if err := json.Unmarshal([]byte(raw), &session); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(session)
		if err != nil {
			t.Fatal(err)
		}
		var before, after struct {
			Capabilities map[string]any `json:"capabilities"`
		}
		if err := json.Unmarshal([]byte(raw), &before); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &after); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"takeover", "rewind", "rewind_code"} {
			if !reflect.DeepEqual(before.Capabilities[key], after.Capabilities[key]) {
				t.Errorf("%s changed %s: %v -> %v", raw, key, before.Capabilities[key], after.Capabilities[key])
			}
		}
	}
}

// A row's permission modes (#8261) go as the list of Persona's ids, in PermissionModes' order, and only when there are
// some; reading them keeps the ids Persona knows and takes anything that is not a list of strings for none, never an
// error that would lose the rest of the row. The set stays one byte in the bools' padding: SessionCapabilities is no
// larger for it, and it and Launcher stay comparable.
func TestPermissionModesGoAsAList(t *testing.T) {
	var s Session
	raw := `{"session_id":"s","title":"T","capabilities":{"permission_modes":["bypassPermissions","yolo","default","default"]}}`
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	if s.Title != "T" || s.Capabilities.PermissionModes != PermissionModeSetOf("default", "bypassPermissions") ||
		!s.Capabilities.PermissionModes.Has("bypassPermissions") || s.Capabilities.PermissionModes.Has("plan") {
		t.Fatalf("read %+v", s.Capabilities)
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	_ = json.Unmarshal(encoded, &out)
	if got := string(out.Capabilities["permission_modes"]); got != `["default","bypassPermissions"]` {
		t.Fatalf("sent %s", encoded)
	}
	for _, raw := range []string{`"default"`, `{"a":1}`, `[1,2]`, `null`} {
		var c SessionCapabilities
		if err := json.Unmarshal([]byte(`{"read":true,"permission_modes":`+raw+`}`), &c); err != nil || !c.Read || c.PermissionModes != 0 {
			t.Fatalf("%s: %+v %v", raw, c, err)
		}
	}
	if b, _ := json.Marshal(SessionCapabilities{Read: true}); string(b) != `{"read":true,"send":false,"interrupt":false,"approve":false,"terminal":false,"attach":false}` {
		t.Fatalf("none sent as %s", b)
	}
	l, _ := json.Marshal(Launcher{Name: "grok", DisplayName: "Grok", PermissionModes: PermissionModeSetOf("default")})
	if string(l) != `{"name":"grok","base_url":"","model":"","permission_modes":["default"],"display_name":"Grok"}` {
		t.Fatalf("launcher %s", l)
	}
	// In the bools' padding: the first word, which the six bools leave room in, so the struct grows by nothing.
	if f, _ := reflect.TypeFor[SessionCapabilities]().FieldByName("PermissionModes"); f.Offset+f.Type.Size() > 8 {
		t.Fatalf("PermissionModes at byte %d, past the bools' word", f.Offset)
	}
	// Comparable, or this does not build: code compares launchers (models_launchers_push.go).
	var a, b Launcher
	var c, d SessionCapabilities
	if a != b || c != d {
		t.Fatal("zero values differ")
	}
}
