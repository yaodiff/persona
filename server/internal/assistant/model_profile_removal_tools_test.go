package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func setUpProfileRemovalTools(t *testing.T, s *Service, f *fakeHub, st *downStore) {
	t.Helper()
	f.mu.Lock()
	f.hello.Locale = "en"
	f.mu.Unlock()
	storeProfiles(t, st,
		ModelProfile{ID: "other", Name: "Other"},
		ModelProfile{ID: "daily", Name: "Daily",
			Purposes:  map[string]ModelBinding{"agent.new": {Provider: "launcher.gw", Model: "profile-agent"}},
			Launchers: map[string]LauncherModels{"gw": {Model: profileValue("profile-main")}}})
	if _, err := s.applyModelProfile(context.Background(), "daily", ProfileActor{Kind: "hub"}); err != nil {
		t.Fatal(err)
	}
}

func TestModelProfileRemovalMCPPhoneApproval(t *testing.T) {
	for _, tc := range []struct {
		tool, action string
		profiles     int
		rollbacks    int
	}{
		{"model_profile_deactivate", "deactivate", 2, 1},
		{"model_profile_clear", "clear", 0, 0},
	} {
		t.Run(tc.action, func(t *testing.T) {
			s, f, st := managedProfileService(t)
			setUpProfileRemovalTools(t, s, f, st)
			before := managedBusinessSnapshot(t, s, st)
			args := map[string]any{}
			checkCard := func(card proto.Interaction) {
				if card.ToolName != tc.tool || !strings.Contains(card.Summary, "Daily") ||
					!strings.Contains(card.Summary, "Current model settings will stay the same.") {
					t.Fatalf("confirmation card: %+v", card)
				}
				if got := managedBusinessSnapshot(t, s, st); !reflect.DeepEqual(got, before) {
					t.Fatal("model profile tool wrote settings before approval")
				}
			}
			denied := runManagedProfileTool(t, s, tc.tool, args, false, checkCard, nil)
			if output := managedToolText(t, denied); !strings.Contains(output, "declined") {
				t.Fatalf("denied tool result: %s", output)
			}
			if got := managedBusinessSnapshot(t, s, st); !reflect.DeepEqual(got, before) {
				t.Fatal("declining confirmation changed settings")
			}
			approved := runManagedProfileTool(t, s, tc.tool, args, true, checkCard, nil)
			var v ModelProfilesView
			if text := managedToolText(t, approved); json.Unmarshal([]byte(text), &v) != nil ||
				v.Active != "" || len(v.Profiles) != tc.profiles || len(v.Rollbacks) != tc.rollbacks ||
				len(v.Events) != 2 || v.Events[0].Action != tc.action || v.Events[0].By.Kind != "assistant" {
				t.Fatalf("approved tool result: %s", text)
			}
			after := managedBusinessSnapshot(t, s, st)
			for key, value := range before {
				if key != modelProfilesKey && key != modelProfileStateKey && after[key] != value {
					t.Fatalf("approved profile removal changed live setting %q", key)
				}
			}
		})
	}
}

func TestModelProfileRemovalMCPExactApproval(t *testing.T) {
	for _, tool := range []string{"model_profile_deactivate", "model_profile_clear"} {
		t.Run(tool, func(t *testing.T) {
			s, f, st, book := ownerBuiltinService(t)
			setUpProfileRemovalTools(t, s, f, st)
			before := managedBusinessSnapshot(t, s, st)
			items := []map[string]any{ownerBuiltinInput(tool, map[string]any{})}

			denied, _, _ := prepareOwnerBuiltins(t, s, book, items, false)
			if result := callOwnerBuiltin(t, s, denied.Items[0]); !result.IsError {
				t.Fatal("the denied exact call bypassed its confirmation")
			}
			if got := managedBusinessSnapshot(t, s, st); !reflect.DeepEqual(got, before) {
				t.Fatal("declining the batch changed model settings")
			}

			approved, _, _ := prepareOwnerBuiltins(t, s, book, items, true)
			item := approved.Items[0]
			if item.Revision == nil || *item.Revision != 1 || !strings.Contains(item.Display, "Daily") {
				t.Fatalf("approved item omitted its target or revision: %+v", item)
			}
			if out := managedToolText(t, callOwnerBuiltin(t, s, item)); !strings.Contains(out, "batch item 1/1 completed") {
				t.Fatalf("exact call did not complete: %s", out)
			}
			if profilesOf(t, s).Active != "" {
				t.Fatal("the approved exact call left a profile in use")
			}
			after := managedBusinessSnapshot(t, s, st)
			if result := callOwnerBuiltin(t, s, item); !result.IsError ||
				!reflect.DeepEqual(managedBusinessSnapshot(t, s, st), after) {
				t.Fatal("the exact call replayed")
			}
		})
	}
}

func TestModelProfileRemovalMCPRejectsStaleApproval(t *testing.T) {
	s, f, st, book := ownerBuiltinService(t)
	setUpProfileRemovalTools(t, s, f, st)
	items := []map[string]any{ownerBuiltinInput("model_profile_clear", map[string]any{})}
	batch, _, _ := prepareOwnerBuiltins(t, s, book, items, true)
	if _, err := s.saveModelProfile(context.Background(), nil, "later", ProfileActor{Kind: "hub"}, func(p *ModelProfile, _ bool) error {
		p.Name = "Saved after the card"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := managedBusinessSnapshot(t, s, st)
	if result := callOwnerBuiltin(t, s, batch.Items[0]); !result.IsError {
		t.Fatal("the old confirmation cleared a newly saved profile")
	}
	if got := managedBusinessSnapshot(t, s, st); !reflect.DeepEqual(got, before) {
		t.Fatal("a stale approval changed model settings")
	}
}

func TestModelProfileDeactivateMCPChecksActiveID(t *testing.T) {
	s, f, st, book := ownerBuiltinService(t)
	setUpProfileRemovalTools(t, s, f, st)
	ctx := context.Background()
	if _, err := s.saveModelProfile(ctx, nil, "other", ProfileActor{Kind: "hub"}, func(p *ModelProfile, _ bool) error {
		p.Name = "Daily"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	items := []map[string]any{ownerBuiltinInput("model_profile_deactivate", map[string]any{})}
	batch, _, _ := prepareOwnerBuiltins(t, s, book, items, true)
	if _, err := s.applyModelProfile(ctx, "other", ProfileActor{Kind: "hub"}); err != nil {
		t.Fatal(err)
	}
	before := managedBusinessSnapshot(t, s, st)
	if result := callOwnerBuiltin(t, s, batch.Items[0]); !result.IsError {
		t.Fatal("the confirmation deactivated a different profile with the same name")
	}
	if got := managedBusinessSnapshot(t, s, st); !reflect.DeepEqual(got, before) {
		t.Fatal("a stale approval changed model settings")
	}
}
