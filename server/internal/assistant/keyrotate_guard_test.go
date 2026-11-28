package assistant

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
)

// The launchers' keys are sealed in settings (launchersManagedKey): the key rotation re-seals the fields listed for it
// in db.SealedSettings, so every sealed field of storedLauncher must be there.
func TestTheLaunchersSealedFieldsAreReSealed(t *testing.T) {
	i := slices.IndexFunc(db.SealedSettings(), func(s db.SealedSetting) bool { return s.Key == launchersManagedKey })
	if i < 0 || !db.SealedSettings()[i].List {
		t.Fatalf("%s is not a list in db.SealedSettings", launchersManagedKey)
	}
	fields := db.SealedSettings()[i].Fields
	sealed := sealedJSONFields(reflect.TypeOf(storedLauncher{}))
	if len(sealed) == 0 {
		t.Fatal("no sealed field found in storedLauncher")
	}
	for _, name := range sealed {
		if !slices.Contains(fields, name) {
			t.Errorf("storedLauncher's %s is sealed and not re-sealed by the key rotation", name)
		}
	}
}

// A provider's keys for single computers are sealed in their own setting: the rotation re-seals them too.
func TestTheComputersKeysAreReSealed(t *testing.T) {
	i := slices.IndexFunc(db.SealedSettings(), func(s db.SealedSetting) bool { return s.Key == launchersMachineKeysKey })
	if i < 0 || !db.SealedSettings()[i].List {
		t.Fatalf("%s is not a list in db.SealedSettings", launchersMachineKeysKey)
	}
	for _, name := range sealedJSONFields(reflect.TypeOf(machineKey{})) {
		if !slices.Contains(db.SealedSettings()[i].Fields, name) {
			t.Errorf("machineKey's %s is sealed and not re-sealed by the key rotation", name)
		}
	}
}

// sealedJSONFields are the JSON names of typ's fields that say they hold a sealed value, embedded structs' too (their
// fields are the object's in JSON).
func sealedJSONFields(typ reflect.Type) []string {
	var out []string
	for i := range typ.NumField() {
		f := typ.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct {
			out = append(out, sealedJSONFields(f.Type)...)
			continue
		}
		if strings.HasSuffix(name, "_sealed") || strings.HasSuffix(name, "_enc") || name == "enc" {
			out = append(out, name)
		}
	}
	return out
}

// Every place in the server that seals, or makes a key box, counted per file (package secretkey itself aside). A new
// one fails here: if it seals with the manager's secret.key, put where it keeps what it seals into db.sealedTables or
// db.sealedSettings (the key rotation re-seals only those, then deletes the old key), and then the count. Values that
// never reach the database count too (the mail OAuth state in a URL).
func TestPlacesThatSealAreKnown(t *testing.T) {
	want := map[string]int{
		// the manager's secret.key
		"internal/assistant/service.go":                2, // its box; and reg.Seal(), which closes the kernel's registry and holds no key
		"internal/assistant/models_launchers.go":       2, // sealKey: launchers.managed
		"internal/assistant/models_launchers_save.go":  3, // its calls: launchers.managed
		"internal/assistant/models_import.go":          1, // launchers.managed: key, headers and env
		"internal/assistant/models_launchers_scope.go": 1, // launchers.machine_keys
		"internal/assistant/launcher_secrets.go":       1, // sealKey: a reference in key_sealed, launchers.managed / launchers.machine_keys
		"internal/assistant/keyrotate.go":              1, // the rotation itself
		"internal/assistant/execution_channels.go":     2, // execution.channels (and its ledger): key and headers
		"internal/extmgr/manager.go":                   3, // its box, sealKey
		"internal/extmgr/extensions.go":                1, // hubSealer: the registry's plugin_secrets
		"internal/extmgr/bots.go":                      1, // bots.feishu
		"internal/extmgr/bots_register.go":             2, // bots.feishu: the registration's device code and app secret
		"internal/extmgr/larksync_host.go":             1, // larkSyncHost.SealSecret: bots.feishu.sync, .sync.bots and .sync.bindings (user tokens)
		"internal/extmgr/connectors.go":                1, // connector_secrets
		"internal/extmgr/secret_kept.go":               1, // secrets.launcher_kept: the launcher and extension secrets a provider gave
		"internal/extmgr/secret_words.go":              1, // secrets.described: references' words a provider gave
		"internal/extmgr/extension_market_edit.go":     1, // plugin_markets.token_sealed
		"internal/extmgr/extension_market_routes.go":   1, // plugin_markets.token_sealed
		"internal/extension/registry_settings.go":      1, // plugin_secrets
		"internal/modules/mail/store.go":               4, // its box, encrypt and its calls: mail_accounts
		"internal/modules/mail/oauth.go":               1, // the OAuth state, in the sign-in URL only
		"internal/modules/mail/microsoft.go":           1, // mail.microsoft_client
		// other keys
		"internal/assistant/kb_owner.go":                       1, // handles.key's cipher for the owner record
		"internal/hub/secretkey.go":                            3, // the hub's own secret.key
		"internal/hub/peer_pair.go":                            2, // the hub's key: linked hubs' tokens
		"internal/hub/devices_service.go":                      1, // the hub's key: the computers' connector credentials
		"internal/hub/preview_origin.go":                       1, // the hub's key: self-contained preview session cookies, never stored
		"internal/hub/push_transport.go":                       1, // a phone's push key (pushcrypto)
		"internal/pushcrypto/pushcrypto.go":                    1, // HPKE to a phone
		"internal/webpush/webpush.go":                          1, // RFC 8291 to a browser's subscription keys: a message, never stored
		"internal/proto/secretenv.go":                          1, // HPKE to a node's one-time stage key: a hand-off, never stored
		"internal/proto/secretstore.go":                        1, // HPKE to the provider node's one-time key: write values, never stored by Persona
		"internal/relay/relay.go":                              1, // the push relay's grants
		"internal/modules/screen-share/screenlink/udp_wire.go": 1, // a live screen's UDP packets: keys from the direct route's TLS exporter, never stored
	}
	lineComment := regexp.MustCompile(`(?m)^\s*//.*$`) // comments may name them freely
	seal := regexp.MustCompile(`\.Seal\(|sealKey\(|\.encrypt\(|SealWith\(|secretkey\.New`)
	got := map[string]int{}
	root := filepath.Join("..", "..")
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/secretkey/") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if n := len(seal.FindAllIndex(lineComment.ReplaceAll(b, nil), -1)); n > 0 {
				got[rel] = n
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for f, n := range got {
		if want[f] != n {
			t.Errorf("%s seals in %d places, %d known: if with the manager's secret.key, add where it keeps them to "+
				"db.sealedTables or db.sealedSettings; then the count here", f, n, want[f])
		}
	}
	for f := range want {
		if got[f] == 0 {
			t.Errorf("%s no longer seals: drop it here", f)
		}
	}
}
