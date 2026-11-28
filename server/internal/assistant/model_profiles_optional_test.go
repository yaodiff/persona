package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestModelProfilesEmptyCatalogAndFirstSave(t *testing.T) {
	s, f, st := modelService(t)
	ctx := context.Background()
	before, puts := profileStoreSnapshot(st.memStore)
	const path = "/api/v1/model-profiles"
	read := request(s, http.MethodGet, path, "")
	var fresh map[string]json.RawMessage
	if read.Code != http.StatusOK || json.Unmarshal(read.Body.Bytes(), &fresh) != nil ||
		string(fresh["revision"]) != "0" || string(fresh["active"]) != `""` || string(fresh["profiles"]) != "[]" {
		t.Fatalf("fresh catalog: %d %s", read.Code, read.Body.String())
	}
	noOp := request(s, http.MethodPost, path+"/deactivate", "")
	if noOp.Code != http.StatusOK || noOp.Body.String() != read.Body.String() {
		t.Fatalf("deactivating a fresh catalog: %d %s", noOp.Code, noOp.Body.String())
	}
	if s.modelProfileContext(ctx) != nil {
		t.Fatal("snapshot advertised a profile on an unconfigured hub")
	}
	if _, err := s.agentStartTier(ctx, Device{}, "", ""); err != nil {
		t.Fatalf("start without a tier: %v", err)
	}
	if _, err := s.agentStartTier(ctx, Device{}, "dev", ""); err == nil {
		t.Fatal("a tier was resolved without an active profile")
	}
	if r := request(s, http.MethodGet, path+"/performance/candidates", ""); r.Code != http.StatusNotFound {
		t.Fatalf("candidate lookup on a fresh hub: %d %s", r.Code, r.Body.String())
	}
	requireProfileStoreUnchanged(t, st.memStore, before, puts)
	if f.eventTypes() != "" {
		t.Fatal("a read or no-op published a profile change")
	}

	// The action route and an existing valid ID can coexist.
	save := request(s, http.MethodPut, path+"/deactivate", `{"revision":0,"name":"Custom"}`)
	var visible ModelProfilesView
	if save.Code != http.StatusOK || json.Unmarshal(save.Body.Bytes(), &visible) != nil ||
		visible.Revision != 1 || visible.Active != "" || len(visible.Profiles) != 1 ||
		visible.Profiles[0].ID != "deactivate" || visible.Profiles[0].Active || visible.Rollback != nil {
		t.Fatalf("first save: %d %s", save.Code, save.Body.String())
	}
	if live := liveModelProfileSettings(st.memStore); !maps.EqualFunc(live, before, bytes.Equal) {
		t.Fatal("saving the first profile changed live settings")
	}
	if r := request(s, http.MethodPost, path+"/deactivate/apply", ""); r.Code != http.StatusOK || profilesOf(t, s).Active != "deactivate" {
		t.Fatalf("apply with the deactivate ID: %d %s", r.Code, r.Body.String())
	}
}

func liveModelProfileSettings(st *memStore) map[string][]byte {
	values, _ := profileStoreSnapshot(st)
	delete(values, modelProfilesKey)
	delete(values, modelProfileStateKey)
	return values
}

func TestModelProfileRemovalRoutesKeepLiveSettings(t *testing.T) {
	const path = "/api/v1/model-profiles"
	for _, tc := range []struct {
		action, method, suffix, conflict string
		revision                         int64
		profiles                         int
	}{
		{action: "deactivate", method: http.MethodPost, suffix: "/deactivate", revision: 1, profiles: 2},
		{action: "delete", method: http.MethodDelete, suffix: "/daily?revision=1", conflict: "/daily?revision=0", revision: 2, profiles: 1},
		{action: "clear", method: http.MethodDelete, suffix: "?revision=1", conflict: "?revision=0", revision: 2},
	} {
		t.Run(tc.action, func(t *testing.T) {
			s, f, st := modelService(t)
			ctx := context.Background()
			putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "gw", Model: "original-model",
				SmallModel: "small-model", BaseURL: "https://example.invalid", Args: []string{"test-argument"}}})
			storeProfiles(t, st,
				ModelProfile{ID: "other", Name: "Other"},
				ModelProfile{ID: "daily", Name: "Daily",
					Purposes:  map[string]ModelBinding{"assistant": {Provider: "launcher.gw", Model: "chat-model"}},
					Launchers: map[string]LauncherModels{"gw": {Model: profileValue("daily-model")}},
					Tiers:     map[string]ModelTier{"dev": {Launcher: "gw", Model: "dev-model"}}},
			)
			if _, err := s.applyModelProfile(ctx, "other", ProfileActor{Kind: "hub"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.applyModelProfile(ctx, "daily", ProfileActor{Kind: "hub"}); err != nil {
				t.Fatal(err)
			}
			initial := profilesOf(t, s)
			if initial.Active != "daily" || len(initial.Rollbacks) != 2 {
				t.Fatalf("expected an active profile and rollback history: %+v", initial)
			}
			before := liveModelProfileSettings(st.memStore)
			events := f.eventTypes()
			s.live.snap = &prompts.Snapshot{Now: time.Now()}
			if tc.conflict != "" {
				values, puts := profileStoreSnapshot(st.memStore)
				r := request(s, tc.method, path+tc.conflict, "")
				if r.Code != http.StatusConflict {
					t.Fatalf("stale revision allowed: %d %s", r.Code, r.Body.String())
				}
				requireProfileStoreUnchanged(t, st.memStore, values, puts)
				if f.eventTypes() != events || s.live.snap == nil {
					t.Fatal("a conflict published a change")
				}
			}

			r := request(s, tc.method, path+tc.suffix, "")
			var view ModelProfilesView
			if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &view) != nil ||
				view.Active != "" || view.At != nil || view.By != nil || view.Revision != tc.revision ||
				view.Profiles == nil || len(view.Profiles) != tc.profiles {
				t.Fatalf("%s: %d %s", tc.action, r.Code, r.Body.String())
			}
			if len(view.Events) != len(initial.Events)+1 || view.Events[0].Action != tc.action || view.Events[0].By.Kind != "hub" {
				t.Fatalf("missing %s audit event: %+v", tc.action, view.Events)
			}
			if s.live.snap != nil || s.modelProfileContext(ctx) != nil || f.eventTypes() != events+" model_profile" {
				t.Fatal("successful mutation did not invalidate the snapshot and notify clients")
			}
			if got := liveModelProfileSettings(st.memStore); !reflect.DeepEqual(got, before) {
				t.Fatal("removing the active profile changed live settings")
			}
			state, err := s.readModelProfileState(ctx)
			if err != nil || state.Active != "" || !state.At.IsZero() || state.By != (ProfileActor{}) {
				t.Fatalf("persisted active profile metadata: %+v, %v", state, err)
			}
			if tc.action == "clear" {
				if state.Previous != nil || len(state.Rollbacks) != 0 || view.Rollback != nil || len(view.Rollbacks) != 0 {
					t.Fatal("clear kept rollback history")
				}
				// Clearing with no revision is also accepted, including an already empty catalog.
				if r := request(s, http.MethodDelete, path, ""); r.Code != http.StatusOK || profilesOf(t, s).Revision != tc.revision+1 {
					t.Fatalf("clear without revision: %d %s", r.Code, r.Body.String())
				}
			} else if !reflect.DeepEqual(initial.Rollbacks, view.Rollbacks) {
				t.Fatal("removing the active designation discarded rollback history")
			}

			values, puts := profileStoreSnapshot(st.memStore)
			events = f.eventTypes()
			if repeat := request(s, http.MethodPost, path+"/deactivate", ""); repeat.Code != http.StatusOK {
				t.Fatalf("repeat deactivation: %d %s", repeat.Code, repeat.Body.String())
			}
			if after := profilesOf(t, s); after.Active != "" || len(after.Profiles) != tc.profiles {
				t.Fatalf("subsequent read reactivated or recreated profiles: %+v", after)
			}
			requireProfileStoreUnchanged(t, st.memStore, values, puts)
			if f.eventTypes() != events || !strings.HasSuffix(events, "model_profile") {
				t.Fatal("repeat deactivation published a change")
			}
		})
	}
}
