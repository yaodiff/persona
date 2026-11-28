package extmgr

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The manager's sections come from its manager.json; hub.json's copies (kept for an older build) are not read, its
// own keys are.
func TestConfigReadsTheManagerSectionsFromManagerJSON(t *testing.T) {
	dir := t.TempDir()
	hub := `{"listen":":18999","token":"hub-token","public_url":"http://hub.example.com:18999",
		"google":{"client_id":"stale"},"extensions":[{"id":"stale"}],"extension_market":{"proxy":"http://stale.example:3128"},"lark":{"account":7},"inbox":{"rules":"hub rules"}}`
	if err := os.WriteFile(filepath.Join(dir, "hub.json"), []byte(hub), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "managers", "assistant", "manager.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	mine := `{"token":"not-the-hub's","google":{"client_id":"mine"},"bots":{"feishu":{"app_id":"cli_example"}},"extension_market":{"proxy":"socks5://proxy.example:1080","ca_file":"/etc/persona/market-ca.pem"},"inbox":{"rules":"my rules"}}`
	if err := os.WriteFile(file, []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(dir, file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":18999" || cfg.Token != "hub-token" || cfg.PublicURL != "http://hub.example.com:18999" || cfg.Inbox.Rules != "my rules" {
		t.Fatalf("hub.json's own keys: %+v", cfg)
	}
	if cfg.Google.ClientID != "mine" || cfg.Bots.Feishu.AppID != "cli_example" || cfg.ExtensionMarket.Proxy != "socks5://proxy.example:1080" || cfg.ExtensionMarket.CAFile != "/etc/persona/market-ca.pem" {
		t.Fatalf("manager.json's sections: %+v %+v %+v", cfg.Google, cfg.Bots, cfg.ExtensionMarket)
	}
	if len(cfg.Extensions) != 0 || cfg.Lark.Account != 0 {
		t.Fatalf("read hub.json's stale copies: %+v %+v", cfg.Extensions, cfg.Lark)
	}
	// Without a manager.json yet everything moved stays empty (the move runs before, at start).
	cfg, err = loadConfig(dir, filepath.Join(t.TempDir(), "manager.json"))
	if err != nil || cfg.Google.ClientID != "" || cfg.ExtensionMarket.Proxy != "" || cfg.ExtensionMarket.CAFile != "" || cfg.Token != "hub-token" {
		t.Fatalf("%+v %v", cfg, err)
	}
}

// An invalid lark section no longer stops anything: it is said, and not read.
func TestConfigInvalidLarkIsAWarning(t *testing.T) {
	file := filepath.Join(t.TempDir(), "manager.json")
	if err := os.WriteFile(file, []byte(`{"lark":{"account":-1},"google":{"client_id":"mine"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(t.TempDir(), file)
	if err != nil || cfg.Lark.Account != 0 || cfg.Google.ClientID != "mine" || len(cfg.warnings) != 1 {
		t.Fatalf("%+v %v", cfg, err)
	}
}

// A manager.json that does not read: its sections are left out and said, hub.json's part still holds; and
// LoadConfig says why, so the manager does not start without them (assistant.moveConfig).
func TestConfigUnreadableManagerJSONKeepsTheHubPart(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hub.json"), []byte(`{"token":"hub-token","listen":":18999"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"google":`, `[1]`, `{"google":"not an object"}`} {
		file := filepath.Join(t.TempDir(), "manager.json")
		if err := os.WriteFile(file, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadConfig(dir, file)
		if err != nil || cfg.Token != "hub-token" || cfg.Listen != ":18999" || cfg.Google.ClientID != "" || len(cfg.warnings) != 1 {
			t.Fatalf("%s: %+v %v", bad, cfg, err)
		}
		if _, err := LoadConfig(dir, file); err == nil {
			t.Fatalf("%s: LoadConfig let it start", bad)
		}
	}
}

// A gate rule the gate could not use, or hub.json's own part of the wrong shape: New would fall back to an empty
// configuration, so LoadConfig stops the start.
func TestLoadConfigRefusesWhatNewWouldDrop(t *testing.T) {
	for _, c := range []struct{ hub, mine string }{
		{`{"token":"t"}`, `{"connector_gate":[{"connector":"github","action":"*","decision":"denied"}]}`},
		{`{"token":"t","public_url":5}`, `{"lark_gate":[{"method":"CLI","path":"/docs/**","action":"deny"}]}`},
		{`{"token":"t"}`, `{"inbox":{"rules":["x"]}}`},
		{`{"token":"t","listen":18660}`, `{}`},
		{`{"token":"t"}`, ``},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "hub.json"), []byte(c.hub), 0o600); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(t.TempDir(), "manager.json")
		if err := os.WriteFile(file, []byte(c.mine), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(dir, file); err == nil {
			t.Fatalf("%s + %s: LoadConfig let it start", c.hub, c.mine)
		}
	}
}

// LoadConfig lets a manager start with a manager.json that reads, or none yet; an invalid lark section is only a
// warning (lark-cli is off then, nothing opens up).
func TestLoadConfigPassesAReadableFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hub.json"), []byte(`{"token":"hub-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, good := range []string{"", `{}`, `{"lark_gate":[{"method":"CLI","path":"/docs/**","action":"deny"}]}`, `{"lark":{"account":-1}}`} {
		file := filepath.Join(t.TempDir(), "manager.json")
		if good != "" {
			if err := os.WriteFile(file, []byte(good), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if cfg, err := LoadConfig(dir, file); err != nil || cfg.Token != "hub-token" {
			t.Fatalf("%q: %+v %v", good, cfg, err)
		}
	}
}

// The ledger's two settings are read from the manager.json (they are in managerconf.Moved: a key outside it is dropped
// before the manager sees it), hub.json's copies are not, and they reach the places that use them.
func TestConfigReadsTheLedgerSettingsFromManagerJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hub.json"), []byte(`{"token":"hub-token","sent_ledger_days":7,"sent_recall_hours":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "managers", "assistant", "manager.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"sent_ledger_days":30,"sent_recall_hours":48}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, load := range map[string]func(string, string) (*Config, error){"loadConfig": loadConfig, "LoadConfig": LoadConfig} {
		cfg, err := load(dir, file)
		if err != nil || cfg.SentLedgerDays != 30 || cfg.SentRecallHours != 48 {
			t.Fatalf("%s: %+v %v", name, cfg, err)
		}
		if limit, limited := (&Manager{cfg: cfg}).recallWindow(); !limited || limit != 48*time.Hour {
			t.Errorf("%s: the recall window is %v (limited %v)", name, limit, limited)
		}
	}
}

// Legacy trusted_extension_markets still decodes during the deprecation window, emits a startup warning when
// non-empty, never blocks manager startup, and stays quiet when absent or empty (#2656).
func TestConfigLegacyTrustedExtensionMarketsWarnsOnStartup(t *testing.T) {
	tests := []struct {
		name        string
		hubJSON     string
		managerJSON string
		wantValues  []string
		wantWarning bool
	}{
		{
			name:        "non-empty manager setting warns and decodes",
			hubJSON:     `{"token":"hub-token"}`,
			managerJSON: `{"trusted_extension_markets":["https://market.example.com"]}`,
			wantValues:  []string{"https://market.example.com"},
			wantWarning: true,
		},
		{
			name:        "empty manager array stays quiet",
			hubJSON:     `{"token":"hub-token"}`,
			managerJSON: `{"trusted_extension_markets":[]}`,
			wantWarning: false,
		},
		{
			name:        "absent setting stays quiet",
			hubJSON:     `{"token":"hub-token"}`,
			managerJSON: `{}`,
			wantWarning: false,
		},
		{
			name:        "stale hub.json copy is not read and does not warn",
			hubJSON:     `{"token":"hub-token","trusted_extension_markets":["https://stale.example.com"]}`,
			managerJSON: `{}`,
			wantWarning: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "hub.json"), []byte(tt.hubJSON), 0o600); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "managers", "assistant", "manager.json")
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(tt.managerJSON), 0o600); err != nil {
				t.Fatal(err)
			}

			cfg, err := LoadConfig(dir, file)
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if !slices.Equal(cfg.TrustedExtensionMarkets, tt.wantValues) {
				t.Fatalf("TrustedExtensionMarkets = %v, want %v", cfg.TrustedExtensionMarkets, tt.wantValues)
			}

			for pathName, opts := range map[string]Options{
				"LoadConfig+New":  {Dir: dir, Config: cfg},
				"New(ConfigFile)": {Dir: dir, ConfigFile: file},
			} {
				var logs strings.Builder
				opts.Logf = func(format string, args ...any) {
					fmt.Fprintf(&logs, format+"\n", args...)
				}
				New(opts)
				output := logs.String()
				hasWarning := strings.Contains(output, "trusted_extension_markets") &&
					strings.Contains(output, "deprecated") &&
					strings.Contains(output, "sandbox")
				if hasWarning != tt.wantWarning {
					t.Errorf("%s: startup deprecation warning = %v, want %v; logs:\n%s", pathName, hasWarning, tt.wantWarning, output)
				}
			}
		})
	}
}
