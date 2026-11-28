package wire

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCompatAssistantURLDoesNotWeakenSecurity(t *testing.T) {
	for _, target := range []string{"persona://session/machine/session", "persona://task/one", "persona://kb/notes/example.md", "persona://file/machine/%2Ftmp%2Fexample.txt"} {
		legacy := strings.Replace(target, "persona://", "jarvis://", 1)
		if err := CheckAssistantOpen(legacy); err != nil {
			t.Fatalf("%s: %v", legacy, err)
		}
	}
	for _, target := range []string{"jarvis://user:password@task/one", "jarvis://settings", "jarvis://session/a", "jarvis://task/a?x=1", "jarvis://task/a#fragment", "jarvis://kb/%2e%2e/private", "jarvis://kb/a%2Fb", "jarvis://file/a%2Fb/%2Ftmp", "jarvis://task/%0a", "jarvis://task/a\\b"} {
		if err := CheckAssistantOpen(target); err == nil {
			t.Fatalf("unsafe alias accepted: %q", target)
		}
	}
	if _, err := ParseAssistantView([]byte(`{"fallback":"Open task","sections":[{"items":[{"type":"button","title":"Open","action":{"id":"open","open":"jarvis://task/one"}}]}]}`)); err != nil {
		t.Fatal(err)
	}
}

// Extension pages accept the legacy scheme for exactly the session and task links they accept as persona://, and
// strip the same kb and file links.
func TestCompatExtensionOpenAliasMatchesPersona(t *testing.T) {
	for _, target := range []string{"persona://session/machine-1/session-1", "persona://task/todo-1"} {
		legacy := strings.Replace(target, "persona://", "jarvis://", 1)
		if err := CheckExtensionOpen(legacy); err != nil {
			t.Fatalf("%s: CheckExtensionOpen rejected valid target: %v", legacy, err)
		}
		v := viewWithOpen(legacy)
		dropped := StripAssistantFieldsForPage(&v)
		if a := actionAt(v); a == nil || a.Open != legacy {
			t.Fatalf("%s: page open was dropped (%v)", legacy, dropped)
		}
	}
	for _, target := range []string{"persona://kb/projects/notes.md", "persona://file/machine-1/%2Fetc%2Fhosts"} {
		legacy := strings.Replace(target, "persona://", "jarvis://", 1)
		if err := CheckExtensionOpen(legacy); err == nil {
			t.Fatalf("%s: CheckExtensionOpen accepted disallowed target", legacy)
		}
		v := viewWithOpen(legacy)
		dropped := StripAssistantFieldsForPage(&v)
		if a := actionAt(v); a != nil || len(dropped) == 0 || dropped[0] != "assistant_action" {
			t.Fatalf("%s: non-page target survived: %#v (dropped %v)", legacy, a, dropped)
		}
	}
}

func compatLookValue(fallback string) map[string]any {
	return map[string]any{
		"id": "fixture-look", "name": "jarvis", "subtitle": "jarvis-circuit",
		"names":             map[string]any{"en": "jarvis"},
		"avatar":            map[string]any{"sizes": map[string]any{"256": "jarvis.png"}},
		"app_icon_fallback": fallback,
	}
}

func compatManifestValue(looks ...any) map[string]any {
	return map[string]any{
		"manifest_version": float64(1), "id": "fixture-looks", "name": "jarvis", "version": "1.0.0",
		"description": "jarvis-circuit", "publisher": "jarvis",
		"contributes": map[string]any{"looks": looks},
	}
}

func compatJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCompatBuiltinLookAcceptsExactAliasesOnly(t *testing.T) {
	for _, test := range []struct{ input, expected string }{
		{"jarvis", "persona"}, {"jarvis-circuit", "persona"},
		{"persona", "persona"},
	} {
		t.Run(test.input, func(t *testing.T) {
			if got := compatibilityValue("BuiltinLook", test.input); got != test.expected {
				t.Fatalf("got %v, want %s", got, test.expected)
			}
			if err := Validate("BuiltinLook", test.input); err != nil {
				t.Fatal(err)
			}
			if err := ValidateJSON("BuiltinLook", compatJSON(t, test.input)); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, value := range []any{"Jarvis", "jarvis-visor", "jarvis-circuit-extra", "jarvis ", "persona-unknown", "persona-visor", "persona-circuit", float64(1), nil, map[string]any{"app_icon_fallback": "jarvis"}} {
		if got := compatibilityValue("BuiltinLook", value); !reflect.DeepEqual(got, value) {
			t.Fatalf("unexpected alias projection: %#v -> %#v", value, got)
		}
		if err := Validate("BuiltinLook", value); err == nil {
			t.Fatalf("invalid builtin admitted: %#v", value)
		}
	}
	// The embedded canonical schema itself does not gain legacy enum values.
	loadOnce.Do(loadSchemas)
	definition, file := resolve("#/$defs/BuiltinLook", "manifest.schema.json")
	check := checker{}
	check.check(definition, "jarvis", file)
	if len(check.errs) == 0 {
		t.Fatal("legacy value was added to the canonical schema")
	}
}

func TestCompatLookAndManifestProjectionPreserveCallerBytes(t *testing.T) {
	for _, def := range []string{"LookItem", "Manifest"} {
		for _, legacy := range []string{"jarvis", "jarvis-circuit"} {
			t.Run(def+"/"+legacy, func(t *testing.T) {
				look := compatLookValue(legacy)
				var value any = look
				if def == "Manifest" {
					value = compatManifestValue(look, compatLookValue("persona"))
				}
				before := compatJSON(t, value)
				raw := append([]byte(nil), before...)
				if err := Validate(def, value); err != nil {
					t.Fatal(err)
				}
				if err := ValidateJSON(def, raw); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(raw, before) || !bytes.Equal(compatJSON(t, value), before) {
					t.Fatal("validation rewrote caller data or package bytes")
				}
				projection := compatibilityValue(def, value).(map[string]any)
				projectedLook := projection
				if def == "Manifest" {
					projectedLook = projection["contributes"].(map[string]any)["looks"].([]any)[0].(map[string]any)
				}
				if projectedLook["app_icon_fallback"] != canonicalBuiltinLook(legacy) {
					t.Fatal("targeted fallback was not projected")
				}
				if projectedLook["name"] != "jarvis" || projectedLook["subtitle"] != "jarvis-circuit" || projectedLook["names"].(map[string]any)["en"] != "jarvis" || projectedLook["avatar"].(map[string]any)["sizes"].(map[string]any)["256"] != "jarvis.png" {
					t.Fatal("unrelated text or asset paths were rewritten")
				}
				if def == "Manifest" && (projection["name"] != "jarvis" || projection["description"] != "jarvis-circuit" || projection["publisher"] != "jarvis") {
					t.Fatal("manifest metadata was rewritten")
				}
				projectedLook["app_icon_fallback"] = "modified"
				projection["name"] = "modified"
				if def == "Manifest" {
					nested := projection["contributes"].(map[string]any)
					nested["extra"] = true
					nested["looks"].([]any)[0] = nil
				}
				if !bytes.Equal(compatJSON(t, value), before) {
					t.Fatal("rewritten map or slice still aliases the caller's container")
				}
			})
		}
	}
}

func TestCompatTypedLookAndManifestValidation(t *testing.T) {
	look := LookItem{}
	if err := json.Unmarshal(compatJSON(t, compatLookValue("jarvis")), &look); err != nil {
		t.Fatal(err)
	}
	if err := Validate("LookItem", look); err != nil {
		t.Fatal(err)
	}
	if look.AppIconFallback != "jarvis" {
		t.Fatal("typed look mutated")
	}
	manifest := Manifest{}
	if err := json.Unmarshal(compatJSON(t, compatManifestValue(compatLookValue("jarvis-circuit"))), &manifest); err != nil {
		t.Fatal(err)
	}
	before := compatJSON(t, manifest)
	if err := Validate("Manifest", manifest); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(compatJSON(t, manifest), before) {
		t.Fatal("typed manifest mutated")
	}
}

func TestCompatProjectionDoesNotAdmitMalformedLookFields(t *testing.T) {
	for _, value := range []any{nil, float64(1), "jarvis", []any{compatLookValue("jarvis")}} {
		if !reflect.DeepEqual(compatibilityValue("LookItem", value), value) {
			t.Fatal("non-object look altered")
		}
		if err := Validate("LookItem", value); err == nil {
			t.Fatalf("malformed look admitted: %#v", value)
		}
	}
	for _, fallback := range []any{nil, false, float64(1), "jarvis-visor", "Jarvis", []any{"jarvis"}} {
		look := compatLookValue("persona")
		look["app_icon_fallback"] = fallback
		if !reflect.DeepEqual(compatibilityValue("LookItem", look), look) {
			t.Fatal("invalid fallback altered")
		}
		if err := Validate("LookItem", look); err == nil {
			t.Fatalf("invalid fallback admitted: %#v", fallback)
		}
		if err := Validate("Manifest", compatManifestValue(look)); err == nil {
			t.Fatal("invalid nested fallback admitted")
		}
	}
	for _, mutation := range []func(map[string]any){
		func(look map[string]any) { delete(look, "avatar") },
		func(look map[string]any) { delete(look, "app_icon_fallback") },
		func(look map[string]any) { look["unexpected"] = "jarvis" },
		func(look map[string]any) { look["avatar"] = "jarvis" },
	} {
		look := compatLookValue("jarvis")
		mutation(look)
		if err := Validate("LookItem", look); err == nil {
			t.Fatal("legacy fallback bypassed other look constraints")
		}
		if err := Validate("Manifest", compatManifestValue(look)); err == nil {
			t.Fatal("legacy fallback bypassed nested look constraints")
		}
	}
	for _, looks := range []any{nil, "jarvis", map[string]any{"app_icon_fallback": "jarvis"}, []any{nil}, []any{float64(1)}} {
		manifest := compatManifestValue()
		manifest["contributes"].(map[string]any)["looks"] = looks
		if err := Validate("Manifest", manifest); err == nil {
			t.Fatalf("invalid looks container admitted: %#v", looks)
		}
	}
}

func TestCompatProjectionIsLimitedToExactDefinitionAndField(t *testing.T) {
	value := map[string]any{
		"app_icon_fallback": "jarvis",
		"contributes": map[string]any{
			"looks": []any{compatLookValue("jarvis")},
			"other": map[string]any{"app_icon_fallback": "jarvis-circuit"},
		},
	}
	for _, def := range []string{"View", "Component", "InitializeResult", "LookAvatar", "manifest", "lookitem"} {
		if got := compatibilityValue(def, value); !reflect.DeepEqual(got, value) {
			t.Fatalf("unrelated %s projected", def)
		}
	}
	projected := compatibilityValue("Manifest", value).(map[string]any)
	if projected["app_icon_fallback"] != "jarvis" || projected["contributes"].(map[string]any)["other"].(map[string]any)["app_icon_fallback"] != "jarvis-circuit" {
		t.Fatal("projection walked an unrelated field")
	}
	manifest := compatManifestValue(compatLookValue("jarvis"))
	manifest["app_icon_fallback"] = "jarvis"
	if err := Validate("Manifest", manifest); err == nil {
		t.Fatal("projection removed an unknown root field")
	}
}
