package extension

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Validate checks everything a manifest can be checked for on its own (docs/plans/plan-extensions.md "Key decisions"). What depends on
// the hub (the platform's command, engines, ids other plugins use) is the registry's.
func (m *Manifest) Validate() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if m.ManifestVersion != ManifestVersion {
		bad("manifest_version %d: this hub reads %d", m.ManifestVersion, ManifestVersion)
	}
	if !ValidID(m.ID) {
		bad("id %q: a-z0-9_-, at most 32, a letter or digit first", m.ID)
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(m.Name)); n == 0 || n > 40 {
		bad("name: 1 to 40 characters")
	}
	if _, err := ParseVersion(m.Version); err != nil {
		bad("version %q: %v", m.Version, err)
	}
	if m.Homepage != "" {
		if u, err := url.Parse(m.Homepage); err != nil || u.Scheme != "https" || u.Host == "" {
			bad("homepage: an https URL")
		}
	}
	if err := checkTags(m.Tags); err != nil {
		bad("tags: %v", err)
	}
	if m.Engines.Persona != "" {
		if _, err := ParseRange(m.Engines.Persona); err != nil {
			bad("engines.persona: %v", err)
		}
	}
	if (m.Runtime != nil || m.Node != nil) && m.Engines.ExtensionProtocol != Protocol {
		bad("engines.extension_protocol %d: this hub speaks %d", m.Engines.ExtensionProtocol, Protocol)
	}
	if m.Runtime != nil {
		errs = append(errs, m.Runtime.validate()...)
		if m.Runtime.IsDeno() && (m.Node != nil || len(m.Tools) != 0 || m.Permissions.AgentRuntimes || m.Permissions.FullHub()) {
			bad("runtime deno: node programs, command-line tools, agent runtimes and unrestricted hub access are not supported")
		}
	}
	if m.Node != nil {
		errs = append(errs, m.Node.validate(m)...)
	}
	for _, e := range m.ActivationEvents {
		if !slices.Contains([]string{OnStartup, OnCall, OnHTTP}, e) {
			bad("activation_events: %q is not on_startup, on_call or on_http", e)
		}
	}
	if m.Runtime != nil && !m.Runtime.SpeaksRPC() && !slices.Contains(m.ActivationEvents, OnStartup) {
		bad("activation_events: a program that does not speak the protocol (rpc none) runs on_startup")
	}
	if m.Runtime == nil && len(m.ActivationEvents) > 0 {
		bad("activation_events without a runtime")
	}
	if slices.Contains(m.ActivationEvents, OnHTTP) && (m.Runtime == nil || !m.Runtime.HTTP) {
		bad("activation_events: on_http without runtime.http")
	}
	if len(m.Contributes) == 0 && len(m.ExtensionPack) == 0 {
		bad("contributes: nothing")
	}
	errs = append(errs, m.validateModel()...)
	errs = append(errs, m.validateTools()...)
	if u := m.Upgrade; u != nil {
		if u.Backup != "" && u.Backup != BackupAuto && u.Backup != BackupRequired {
			bad("upgrade.backup %q: auto or required", u.Backup)
		}
		if (u.Prepare || u.HealthCheck) && !m.Runtime.SpeaksRPC() {
			bad("upgrade.prepare and upgrade.health_check need a runtime that speaks the protocol")
		}
	}
	for _, name := range sortedKeys(m.Contributes) {
		p, known := Points[name]
		if !known {
			continue // a newer hub's (Lint warns, it does not take effect), or another extension's (validateModel)
		}
		var items []json.RawMessage
		if err := json.Unmarshal(m.Contributes[name], &items); err != nil {
			bad("contributes.%s: a list", name)
			continue
		}
		if len(items) == 0 {
			bad("contributes.%s: empty", name)
		}
		seen := map[string]bool{}
		for _, raw := range items {
			id, err := p.Item(raw)
			if err != nil {
				bad("contributes.%s: %v", name, err)
				continue
			}
			if seen[id] {
				bad("contributes.%s: %q twice", name, id)
			}
			seen[id] = true
		}
		if p.NeedsRPC && !m.Runtime.SpeaksRPC() {
			bad("contributes.%s needs a runtime that speaks the protocol", name)
		}
		if p.NeedsHTTP && (m.Runtime == nil || !m.Runtime.HTTP) {
			bad("contributes.%s needs runtime.http", name)
		}
		if name == "quota" && !m.Permissions.LauncherKeys {
			bad("contributes.quota needs permissions.launcher_keys (the user agrees that it reads quota with launchers' keys)")
		}
		if name == "usage" && !m.Permissions.LauncherKeys {
			for _, u := range m.Usages() {
				if !u.Direct {
					bad("launcher-backed contributes.usage needs permissions.launcher_keys")
					break
				}
			}
		}
		if name == "api" && !slices.Contains(m.Activation(), OnStartup) {
			bad("contributes.api: the app's API is there all the time: activation_events on_startup")
		}
		if name == "api" {
			for _, it := range m.APIs() {
				if len(it.Events) > 0 && !m.Runtime.SpeaksRPC() {
					bad("contributes.api: %s pushes events (api.broadcast) but the plugin does not speak the protocol", it.ID)
				}
			}
		}
		if name == "messaging" {
			if !slices.Contains(m.Activation(), OnStartup) {
				bad("contributes.messaging: the 消息 entry is there all the time and pushes changes: activation_events on_startup")
			}
			pages := map[string]bool{}
			for _, u := range m.UIs() {
				pages[u.ID] = u.Placement == "extension_page" || u.Placement == "plugin_page"
			}
			for _, it := range m.Messaging() {
				if it.SettingsUI != "" && !pages[it.SettingsUI] {
					bad("contributes.messaging: %s: settings_ui %q is not an extension_page of this extension", it.ID, it.SettingsUI)
				}
				for _, l := range it.Links {
					if ui, ok := strings.CutPrefix(l.Opens, "ui:"); ok && !pages[ui] {
						bad("contributes.messaging: %s: a link opens %q, not an extension_page of this extension", it.ID, ui)
					}
				}
			}
		}
		if name == "routine_facts" && !m.Permissions.RoutineFacts {
			bad("contributes.routine_facts needs permissions.routine_facts")
		}
		if name == "error_reports" && !m.Permissions.ErrorReports {
			bad("contributes.error_reports needs permissions.error_reports (the user agrees that it gets the error and crash reports)")
		}
		if name == "repository_providers" {
			// One host is one provider's, in a manifest as across extensions (claimClashes).
			claimed := map[string]string{}
			for _, it := range m.RepositoryProviders() {
				for _, h := range it.Hosts {
					if other, ok := claimed[h]; ok && other != it.ID {
						bad("contributes.repository_providers: the host %q is in both %s and %s", h, other, it.ID)
					}
					claimed[h] = it.ID
				}
			}
		}
		if name == "memory_sources" && len(items) > 1 {
			bad("contributes.memory_sources: one per extension")
		}
		if name == "auth_relay" && !m.Permissions.AuthRelay {
			bad("contributes.auth_relay needs permissions.auth_relay (the user agrees that it completes the authorization links their Agents stop at)")
		}
		if name == "error_reports" && !slices.Contains(m.Activation(), OnStartup) {
			bad("contributes.error_reports: reports are given to a plugin that runs: activation_events on_startup")
		}
		if name == "events" && !slices.Contains(m.Activation(), OnStartup) {
			bad("contributes.events: events are told to a plugin that runs: activation_events on_startup")
		}
		if name == "agent_sources" && !slices.Contains(m.Activation(), OnStartup) {
			bad("contributes.agent_sources: the Agents tab lists it all the time: activation_events on_startup")
		}
		if name == "channel" {
			for _, c := range m.Channels() {
				switch {
				case c.Connector != "" && !slices.Contains(m.Items("connectors"), c.Connector):
					bad("contributes.channel: %s binds through connector %s, which the plugin does not contribute", c.ID, c.Connector)
				case c.Connector == "" && !slices.Contains(m.Activation(), OnStartup):
					bad("contributes.channel: %s has no connector to run with: activation_events on_startup", c.ID)
				}
			}
		}
		if name == "ui" {
			uis := m.UIs()
			for i, u := range uis {
				if u.Placement == "launcher_section" && !m.Permissions.LauncherKeys {
					bad("contributes.ui: %s sits on launchers' pages and reaches their gateways: permissions.launcher_keys", u.ID)
				}
				if u.Opens != "" && !slices.ContainsFunc(uis, func(o UIItem) bool {
					return o.ID == u.Opens && FullPage(o.Placement)
				}) {
					bad("contributes.ui: %s opens %q, which is not a nav_page or extension_page of this extension", u.ID, u.Opens)
				}
				if u.Placement != "inbox_item" || u.Connector == "" {
					continue
				}
				// A plugin draws only the items its own connector brought in: another source's (mail, GitHub…) are
				// not its to read, nor its to draw.
				if !slices.Contains(m.Items("connectors"), u.Connector) {
					bad("contributes.ui: %s draws the inbox items of connector %s, which the plugin does not contribute", u.ID, u.Connector)
				}
				if slices.ContainsFunc(uis[:i], func(o UIItem) bool { return o.Placement == "inbox_item" && o.Connector == u.Connector }) {
					bad("contributes.ui: more than one inbox_item draws connector %s's items", u.Connector)
				}
			}
		}
		if name == "shortcuts" {
			for _, s := range m.Shortcuts() {
				if s.UI != "" && !slices.ContainsFunc(m.UIs(), func(o UIItem) bool { return o.ID == s.UI && FullPage(o.Placement) }) {
					bad("contributes.shortcuts: %s opens %q, which is not a nav_page or extension_page of this extension", s.ID, s.UI)
				}
			}
		}
		if name == "providers" {
			for _, it := range m.Providers() {
				if it.DynamicModels && !m.Runtime.SpeaksRPC() {
					bad("contributes.providers: %s lists its models itself (dynamic_models) but the plugin runs no program that speaks the protocol", it.ID)
				}
			}
		}
	}
	if len(m.Configuration) > 0 {
		if _, err := ParseSchema(m.Configuration); err != nil {
			bad("configuration: %v", err)
		}
	}
	for _, msg := range m.agentFilterSwitches() {
		bad("%s", msg)
	}
	for _, msg := range m.tunnelTargetProblems() {
		bad("%s", msg)
	}
	for _, msg := range m.offByDefaultProblems() {
		bad("%s", msg)
	}
	var entryLists []Field
	if schema, err := ParseSchema(m.Configuration); err == nil {
		entryLists = schema.EntryListFields()
	}
	if len(m.Secrets)+len(entryLists) > SecretsMost {
		bad("secrets: more than %d fixed and entry-list declarations", SecretsMost)
	}
	seen := map[string]bool{}
	for _, s := range m.Secrets {
		if !secretPattern.MatchString(s.Name) {
			bad("secrets: name %q: a-z0-9_, a letter first, at most 32", s.Name)
		}
		if seen[s.Name] {
			bad("secrets: %q twice", s.Name)
		}
		seen[s.Name] = true
		if strings.TrimSpace(s.Title) == "" {
			bad("secrets: %q has no title", s.Name)
		}
	}
	for _, f := range entryLists {
		name := f.Secret.Name
		if seen[name] {
			bad("secrets: %q is declared by both a fixed secret and an entry list (or by two lists)", name)
		}
		seen[name] = true
	}
	if schema, err := ParseSchema(m.Configuration); err == nil {
		for _, u := range m.Usages() {
			if u.RemoteKey == "" {
				continue
			}
			if schema == nil || !slices.ContainsFunc(schema.Fields, func(f Field) bool {
				return f.Name == u.RemoteAddress && f.Type == "string" && f.Format == "uri"
			}) {
				bad("direct usage %q: remote_address must name a URI setting", u.ID)
			}
			if !slices.ContainsFunc(m.Secrets, func(s SecretSpec) bool { return s.Name == u.RemoteKey }) {
				bad("direct usage %q: remote_key must name a fixed secret", u.ID)
			}
		}
	}
	errs = append(errs, m.checkConfigured()...)
	if err := m.Permissions.validate(); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("plugin %s: %w", m.ID, err)
	}
	return nil
}

func (r *Runtime) validate() []error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	paths := map[string]string{"": r.Command.Path}
	if r.Mode != "" && r.Mode != "process" && r.Mode != RuntimeDeno {
		bad("runtime.mode: expected process or deno")
	}
	if r.IsDeno() {
		errs = append(errs, r.validateSource()...)
		paths = nil
	} else if r.Source != nil {
		bad("runtime.source: only supported with mode deno")
	}
	if r.Command.ByPlatform != nil && !r.IsDeno() {
		paths = r.Command.ByPlatform
		if len(paths) == 0 {
			bad("runtime.command: no platform")
		}
	}
	for platform, p := range paths {
		if platform != "" && !platformKey.MatchString(platform) {
			bad("runtime.command: platform %q is not <os>-<arch>", platform)
		}
		if err := checkCommandPath(p); err != nil {
			bad("runtime.command %q: %v", p, err)
		}
	}
	if r.RPC != "" && r.RPC != "stdio" && r.RPC != "none" {
		bad("runtime.rpc %q: stdio or none", r.RPC)
	}
	if !r.SpeaksRPC() && !r.HTTP {
		bad("runtime: rpc none and no http: the hub could not talk to it")
	}
	if res := r.Resources; res != nil {
		if res.MemoryMB != 0 && (res.MemoryMB < ResourcesMemoryMin || res.MemoryMB > ResourcesMemoryMax) {
			bad("runtime.resources.memory_mb %d: %d–%d", res.MemoryMB, ResourcesMemoryMin, ResourcesMemoryMax)
		}
		if res.CPUPercent != 0 && (res.CPUPercent < ResourcesCPUMin || res.CPUPercent > ResourcesCPUMax) {
			bad("runtime.resources.cpu_percent %d: %d–%d", res.CPUPercent, ResourcesCPUMin, ResourcesCPUMax)
		}
	}
	return append(errs, checkEnv("runtime.env", r.Env)...)
}

func checkEnv(at string, env map[string]string) []error {
	var errs []error
	for k := range env {
		if !envPattern.MatchString(k) {
			errs = append(errs, fmt.Errorf("%s: %q is not a variable name", at, k))
		}
		if compat.ReservedEnv(k) {
			errs = append(errs, fmt.Errorf("%s: %q is the hub's to set", at, k))
		}
		// The dynamic loader's variables reach the sandbox's own launcher (bwrap, systemd-run, sandbox-exec), which
		// starts outside the sandbox: a library preloaded there would run unconfined.
		if strings.HasPrefix(k, "LD_") || strings.HasPrefix(k, "DYLD_") {
			errs = append(errs, fmt.Errorf("%s: %q is not allowed (the dynamic loader's)", at, k))
		}
	}
	return errs
}

func (r *NodeRuntime) validate(m *Manifest) []error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if r.Builtin() {
		bad("node.command: %q is only for the hub's own built-in extensions", NodeBuiltin)
	} else if len(r.Command.ByPlatform) == 0 {
		bad("node.command: one program per platform (\"darwin-arm64\": \"node/bin/x\")")
	}
	for platform, p := range r.Command.ByPlatform {
		sys, _, _ := strings.Cut(platform, "-")
		if !platformKey.MatchString(platform) || !slices.Contains(nodeSystems, sys) {
			bad("node.command: platform %q is not darwin-<arch> or linux-<arch>", platform)
		}
		if IsBuiltinCommand(p) {
			bad("node.command: the hub's own binary is not a plugin's program on computers")
		} else if err := checkCommandPath(p); err != nil {
			bad("node.command %q: %v", p, err)
		}
	}
	if r.RPC != "" && r.RPC != "stdio" && r.RPC != "none" {
		bad("node.rpc %q: stdio or none", r.RPC)
	}
	if !r.SpeaksRPC() && !r.HTTP {
		bad("node: rpc none and no http: the hub could not talk to it")
	}
	if r.KeepRunning != "" && r.KeepRunning != KeepLease && r.KeepRunning != KeepAlways {
		bad("node.keep_running %q: lease or always", r.KeepRunning)
	}
	errs = append(errs, checkEnv("node.env", r.Env)...)
	seen := map[string]bool{}
	for _, name := range r.Secrets {
		if !slices.ContainsFunc(m.Secrets, func(s SecretSpec) bool { return s.Name == name }) {
			bad("node.secrets: %q is not one of secrets", name)
		}
		if seen[name] {
			bad("node.secrets: %q twice", name)
		}
		seen[name] = true
	}
	for _, h := range r.Permissions.Network {
		if !hostPattern.MatchString(h) || len(h) > 253 {
			bad("node.permissions.network: %q is not a host name (lowercase, no scheme, port or path; *. allowed first)", h)
		}
	}
	f := m.NodeMachines()
	switch {
	case r.Machines == "":
		bad("node.machines: the configuration field that picks the computers")
	case f == nil:
		bad("node.machines: configuration has no field %q", r.Machines)
	case f.Type != "array" || f.Items == nil || f.Items.Format != "machine":
		bad("configuration.%s: picks the computers: an array of format machine", r.Machines)
	case len(f.Items.OS) == 0 || slices.ContainsFunc(f.Items.OS, func(sys string) bool { return !slices.Contains(r.Systems(), sys) }):
		bad("configuration.%s: x-os %v: node.command has programs for %v", r.Machines, f.Items.OS, r.Systems())
	}
	return errs
}

// checkCommandPath: a path inside the package (relative, no ..), or the hub's own binary.
func checkCommandPath(p string) error {
	switch {
	case p == "":
		return errors.New("empty")
	case IsBuiltinCommand(p):
		return nil
	case strings.HasPrefix(p, "/") || strings.Contains(p, `\`):
		return errors.New("must be relative to the package")
	case path.Clean(p) != p || p == "." || strings.HasPrefix(p, "../") || p == "..":
		return errors.New("must stay inside the package")
	}
	return nil
}

// Lint are warnings that do not stop an install: extension points this hub does not have, the hub's full token asked
// the old way, configuration properties that look like secrets.
func (m *Manifest) Lint() []string {
	var out []string
	for _, name := range sortedKeys(m.Contributes) {
		if _, ok := ItemOf(name); !ok {
			out = append(out, i18n.T(i18n.En, "extension-status.unknown_point", i18n.Args{"name": name}))
		}
	}
	if m.Permissions.FullHub() {
		out = append(out, i18n.T(i18n.En, "extension-status.legacy_full_token"))
	}
	if s, err := ParseSchema(m.Configuration); err == nil && s != nil {
		for _, f := range s.Fields {
			if looksSecret(f.Name) {
				out = append(out, i18n.T(i18n.En, "extension-status.plaintext_secret", i18n.Args{"name": f.Name}))
			}
			if f.EntryList() {
				for _, sub := range f.Items.Fields {
					if looksSecret(sub.Name) {
						out = append(out, i18n.T(i18n.En, "extension-status.plaintext_secret", i18n.Args{"name": f.Name + "[]." + sub.Name}))
					}
				}
			}
		}
	}
	return out
}

func looksSecret(name string) bool {
	n := strings.ToLower(name)
	for _, w := range []string{"password", "passwd", "token", "secret", "api_key", "apikey", "private_key"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}
