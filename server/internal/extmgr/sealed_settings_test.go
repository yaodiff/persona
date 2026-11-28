package extmgr

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
)

// The bot's app secret is sealed in settings (botSettingsKey): the key rotation re-seals the fields listed for it in
// db.SealedSettings, so every sealed field of FeishuBotSettings must be there.
func TestTheBotsSealedFieldsAreReSealed(t *testing.T) {
	i := slices.IndexFunc(db.SealedSettings(), func(s db.SealedSetting) bool { return s.Key == botSettingsKey })
	if i < 0 || db.SealedSettings()[i].List {
		t.Fatalf("%s is not an object in db.SealedSettings", botSettingsKey)
	}
	typ := reflect.TypeOf(FeishuBotSettings{})
	n := 0
	for i2 := range typ.NumField() {
		f := typ.Field(i2)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if strings.HasSuffix(name, "_enc") || strings.HasSuffix(name, "_sealed") {
			n++
			if !slices.Contains(db.SealedSettings()[i].Fields, name) {
				t.Errorf("FeishuBotSettings.%s (%s) is sealed and not re-sealed by the key rotation", f.Name, name)
			}
		}
	}
	if n == 0 {
		t.Fatal("no sealed field found in FeishuBotSettings")
	}
}

// The launcher keys a provider gave are kept sealed in settings (launcherKeptKey, secret_kept.go): the key rotation
// re-seals the fields listed for it in db.SealedSettings, so every sealed field of storedKeptSecret must be there.
func TestKeptLauncherKeysAreReSealed(t *testing.T) {
	i := slices.IndexFunc(db.SealedSettings(), func(s db.SealedSetting) bool { return s.Key == launcherKeptKey })
	if i < 0 || !db.SealedSettings()[i].List {
		t.Fatalf("%s is not a list in db.SealedSettings", launcherKeptKey)
	}
	typ := reflect.TypeOf(storedKeptSecret{})
	n := 0
	for j := range typ.NumField() {
		f := typ.Field(j)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if strings.HasSuffix(name, "_enc") || strings.HasSuffix(name, "_sealed") {
			n++
			if !slices.Contains(db.SealedSettings()[i].Fields, name) {
				t.Errorf("storedKeptSecret.%s (%s) is sealed and not re-sealed by the key rotation", f.Name, name)
			}
		}
	}
	if n == 0 {
		t.Fatal("no sealed field found in storedKeptSecret")
	}
}
