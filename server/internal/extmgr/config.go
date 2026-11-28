package extmgr

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/nwverse/persona/server/internal/cliport"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extension/denohost"
	"github.com/nwverse/persona/server/internal/githubwire"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/imessagewire"
	"github.com/nwverse/persona/server/internal/lark"
	"github.com/nwverse/persona/server/internal/managerconf"
	"github.com/nwverse/persona/server/internal/proto"
)

// Config is the manager's configuration for the extensions, connectors, bots and the inbox: Listen, Token and
// PublicURL are hub.json's (the hub's own file, read as it is), the rest the manager's own manager.json's
// (managerconf.Moved, docs/operations/config.md "manager.json").
type Config struct {
	Listen    string `json:"listen"`
	Token     string `json:"token"`
	PublicURL string `json:"public_url"`
	// Lark: the lark-cli path and the Feishu account (docs/extensions/feishu.md).
	Lark lark.Config `json:"lark"`
	// LarkGate and ConnectorGate: the rules for the assistant's Feishu writes and the connectors' writes
	// (internal/connector/gate.go).
	LarkGate      []proto.LarkRule `json:"lark_gate,omitempty"`
	ConnectorGate []connector.Rule `json:"connector_gate,omitempty"`
	// ConnectorAutoConfirm: opt-in hub-side policies allowing verified extension installations to participate in
	// guarded conditional auto-confirm for exact actions (all off by default; internal/connector/auto_confirm.go).
	ConnectorAutoConfirm []connector.AutoConfirmPolicy `json:"connector_auto_confirm,omitempty"`
	GitHub               *githubwire.Config            `json:"github,omitempty"`
	IMessage             *imessagewire.Config          `json:"imessage,omitempty"`
	// Google and Microsoft: mail's sign-ins (docs/extensions/mail.md).
	Google           GoogleConfig      `json:"google"`
	Microsoft        MicrosoftConfig   `json:"microsoft,omitempty"`
	Inbox            InboxConfig       `json:"inbox"`
	Bots             BotsConfig        `json:"bots,omitempty"`
	Extensions       []ExtensionConfig `json:"extensions,omitempty"`
	LegacyExtensions []ExtensionConfig `json:"plugins,omitempty"`
	// TrustedExtensionMarkets is deprecated (#2656): retained for decoding during the warning window only, grants no sandbox exemption.
	TrustedExtensionMarkets []string `json:"trusted_extension_markets,omitempty"`
	// ExtensionMarket configures outbound marketplace reads and package downloads.
	ExtensionMarket ExtensionMarketConfig `json:"extension_market,omitempty"`
	// ExtensionRuntime pins the shared engine; missing/unsupported enforcement blocks source activation.
	ExtensionRuntime *denohost.Config  `json:"extension_runtime,omitempty"`
	UIPageSessions   PageSessionLimits `json:"ui_page_sessions,omitempty"`

	// SentLedgerDays is how many days the ledger of what was written as the user is kept (sent_ledger.go): 0 or
	// absent is 90, below 0 keeps it for good.
	SentLedgerDays int `json:"sent_ledger_days,omitempty"`
	// SentRecallHours is how many hours after a send it can be taken back (sent_recall.go): 0 or absent is 24, below 0
	// is no limit; a connector may allow less.
	SentRecallHours int `json:"sent_recall_hours,omitempty"`
	// RemoteImages is manager.json's remote_images, read here for the avatars route's fetches (avatars.go) as the
	// assistant reads it for /remote-image: its doh.
	RemoteImages RemoteImagesConfig `json:"remote_images,omitempty"`

	dir      string
	warnings []string // said once when the manager starts (loadConfig)
	// fileErr is why the manager.json's sections were not read (LoadConfig): the manager does not start with it.
	fileErr error
}

// ExtensionMarketConfig is the extension marketplace's outbound network configuration.
type ExtensionMarketConfig struct {
	// Proxy is an http:// or socks5:// URL used for catalog reads, package downloads and HTTPS git clones.
	Proxy string `json:"proxy,omitempty"`
	// CAFile is the path to a PEM bundle appended to the system roots for the marketplace HTTP client only.
	CAFile string `json:"ca_file,omitempty"`
}

// RemoteImagesConfig is manager.json's remote_images: DoH is the DNS-over-HTTPS JSON API asked for a host's real
// addresses behind a fake-IP proxy (nil: remoteimage.DefaultDoH; "": none, and such pictures are refused).
type RemoteImagesConfig struct {
	DoH *string `json:"doh,omitempty"`
}

// GoogleConfig is the iOS OAuth client behind Gmail's "Sign in with Google" (docs/extensions/mail.md).
type GoogleConfig struct {
	ClientID string `json:"client_id"`
	// Proxy is an http:// (CONNECT) or socks5:// proxy for Gmail IMAP / SMTP and Google's OAuth / Gmail API.
	Proxy string `json:"proxy,omitempty"`
}

// MicrosoftConfig is Outlook's "Sign in with Microsoft" (docs/extensions/mail.md "Outlook: Sign In with Microsoft").
type MicrosoftConfig struct {
	ClientID    string `json:"client_id,omitempty"`
	RedirectURI string `json:"redirect_uri,omitempty"`
}

// loadConfig reads dir's hub.json with the environment over it as the hub does (PERSONA_PUBLIC_URL, PERSONA_LISTEN),
// and the manager's sections from file (its manager.json): hub.json's copies of those are not read. file "": all of
// it from hub.json (tests). A missing file is an empty one.
func loadConfig(dir, file string) (*Config, error) {
	cfg := &Config{Listen: ":18660", dir: dir}
	b, err := managerconf.ReadFile(filepath.Join(dir, "hub.json"))
	switch {
	case err == nil && b != nil:
		if file != "" {
			o, err := managerconf.ParseObject(b)
			if err != nil {
				return cfg, fmt.Errorf("parse %s: %w", filepath.Join(dir, "hub.json"), err)
			}
			b = managerconf.Without(o, managerconf.Moved)
		}
		if err := json.Unmarshal(b, cfg); err != nil {
			return cfg, fmt.Errorf("parse %s: %w", filepath.Join(dir, "hub.json"), err)
		}
	case err != nil:
		return cfg, err
	}
	if file != "" {
		// A manager.json that does not read leaves its sections out, and says so: hub.json's part still holds.
		if mine, err := managerconf.Sections(file); err != nil {
			cfg.fileErr = err
			cfg.warnings = append(cfg.warnings, fmt.Sprintf("%v: its sections are not read until it is fixed", err))
		} else if err := json.Unmarshal(mine, cfg); err != nil {
			cfg = cfgWithout(cfg)
			cfg.fileErr = fmt.Errorf("parse %s: %w", file, err)
			cfg.warnings = append(cfg.warnings, fmt.Sprintf("parse %s: %v: its sections are not read until it is fixed", file, err))
		}
	}
	if v := identity.Getenv("PERSONA_PUBLIC_URL"); v != "" {
		cfg.PublicURL = v
	}
	if v := identity.Getenv("PERSONA_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if err := connector.ValidateRules(cfg.ConnectorGate); err != nil {
		cfg.ConnectorGate = nil
		where := "hub.json"
		if file != "" {
			where = file
		}
		return cfg, fmt.Errorf("%s: connector_gate: %w", where, err)
	}
	if err := connector.ValidateAutoConfirmPolicies(cfg.ConnectorAutoConfirm); err != nil {
		cfg.ConnectorAutoConfirm = nil
		where := "hub.json"
		if file != "" {
			where = file
		}
		return cfg, fmt.Errorf("%s: connector_auto_confirm: %w", where, err)
	}
	if err := cfg.Lark.Validate(); err != nil { // was the hub's check at start; now the manager's, which uses it
		cfg.warnings = append(cfg.warnings, err.Error()+": lark is not read")
		cfg.Lark = lark.Config{}
	}
	if cfg.Lark.CLI != nil && !cliport.InBuild() { // a build without the lark module (persona_without_lark)
		cfg.warnings = append(cfg.warnings, "lark.cli is not read: "+cliport.ErrUnavailable.Error())
		cfg.Lark.CLI = nil
	}
	if cfg.Lark.LegacyGatewayConfigured() {
		cfg.warnings = append(cfg.warnings, "lark.container / token / base_url are not read any more: Feishu through a Lark "+
			"gateway is an extension's now; set them in its settings (docs/extensions/feishu.md)")
	}
	if len(cfg.TrustedExtensionMarkets) > 0 {
		cfg.warnings = append(cfg.warnings, "trusted_extension_markets is deprecated and grants no sandbox exemptions; "+
			"remove it and use explicit per-extension sandbox settings instead "+
			"(trusted_extension_markets will stop being decoded after the one-release warning window; "+
			"docs/extensions/extensions.md \"Sandbox\" and \"Naming and compatibility\")")
	}
	return cfg, nil
}

// LoadConfig is the configuration of a manager about to start, from the manager.json at file and dir's hub.json (as
// loadConfig reads them), for Options.Config; or why it must not start: a file that does not read, sections that do
// not fit, a connector_gate rule the gate could not use. Started anyway, it would run without them (New falls back to
// an empty configuration), the user's gate rules (lark_gate, connector_gate) among them, and the node would fall back
// to its own, which let every read through (assistant.moveConfig). Read once, so what was checked is what runs.
func LoadConfig(dir, file string) (*Config, error) {
	cfg, err := loadConfig(dir, file)
	if err != nil {
		return nil, err
	}
	if cfg.fileErr != nil {
		return nil, cfg.fileErr
	}
	return cfg, nil
}

// cfgWithout is cfg with only hub.json's part (after manager.json's sections failed to unmarshal halfway).
func cfgWithout(cfg *Config) *Config {
	return &Config{Listen: cfg.Listen, Token: cfg.Token, PublicURL: cfg.PublicURL, dir: cfg.dir, warnings: cfg.warnings}
}
