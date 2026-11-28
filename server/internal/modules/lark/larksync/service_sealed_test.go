package larksync

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/larksyncwire"
)

// Every sealed field of the sync's settings structs matches larksyncwire.Sealed*Fields (which db.SealedSettings
// re-seals on key rotation), and syncAccount holds nothing secret.
func TestLarkSyncSealedFieldsMatchContract(t *testing.T) {
	for _, c := range []struct {
		key    string
		listed []string
		typ    reflect.Type
	}{
		{prefsKey, larksyncwire.SealedPrefsFields, reflect.TypeOf(syncPrefs{})},
		{botsKey, larksyncwire.SealedBotsFields, reflect.TypeOf(syncBot{})},
		{bindingsKey, larksyncwire.SealedBindingsFields, reflect.TypeOf(syncBinding{})},
	} {
		var sealed []string
		for f := range c.typ.NumField() {
			name, _, _ := strings.Cut(c.typ.Field(f).Tag.Get("json"), ",")
			if strings.HasSuffix(name, "_enc") || strings.HasSuffix(name, "_sealed") {
				sealed = append(sealed, name)
				if !slices.Contains(c.listed, name) {
					t.Errorf("%s.%s is sealed and not in larksyncwire sealed fields", c.key, name)
				}
			}
		}
		for _, name := range c.listed {
			if !slices.Contains(sealed, name) {
				t.Errorf("larksyncwire lists %s.%s, which %s does not have", c.key, name, c.typ)
			}
		}
	}
	typ := reflect.TypeOf(syncAccount{})
	for f := range typ.NumField() {
		if name := typ.Field(f).Tag.Get("json"); strings.Contains(name, "_enc") || strings.Contains(name, "token") {
			t.Errorf("an account keeps %s", name)
		}
	}

	// What is sealed is really there, sealed, where key rotation looks.
	w := newServiceWorld(t)
	w.addSyncBot(t, "bsync", "cli_sync", larksyncwire.TierDocs)
	w.adopt(t, "bsync", "ou_a", "plain-access", "plain-refresh", time.Hour)
	var bindings []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(w.store.raw(bindingsKey)), &bindings); err != nil || len(bindings) != 1 {
		t.Fatalf("bindings: %v %s", err, w.store.raw(bindingsKey))
	}
	for _, f := range []string{"access_token_enc", "refresh_token_enc"} {
		var v string
		_ = json.Unmarshal(bindings[0][f], &v)
		if plain, err := w.svc.open(v); err != nil || !strings.HasPrefix(plain, "plain-") {
			t.Fatalf("%s: %q %v", f, plain, err)
		}
	}
	noSecretIn(t, "bindings", w.store.raw(bindingsKey), "plain-access", "plain-refresh")
	noSecretIn(t, "bots", w.store.raw(botsKey), "cli_sync-secret")
}
