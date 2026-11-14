package main

import (
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strconv"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/link"
)

// The independently installed Mac Link is a launchd user agent. It owns neither the Electron desktop's lifetime nor
// the resident node's separate com.persona.node agent. Its executable is the stable ~/.persona/bin/persona-link that
// the desktop's fixed setup IPC may invoke; a running agent's old inode can finish while an upgrade replaces the file.

const linkLabel = "com.persona.link"

func (e *env) linkPlistPath() string {
	return filepath.Join(e.home, "Library", "LaunchAgents", linkLabel+".plist")
}

func (e *env) linkLaunchdDomain() string {
	return "gui/" + strconv.Itoa(e.uid)
}

// linkPlist keeps only the connector under this agent. JSON status lines have no consumer in a service, so stdout is
// discarded; link.status.json remains the public status contract and --log owns the rotated, readable connector log.
func linkPlist(bin, logPath string) string {
	x := html.EscapeString
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>` + linkLabel + `</string>
  <key>ProgramArguments</key><array><string>` + x(bin) + `</string><string>run</string><string>--log</string><string>` + x(logPath) + `</string></array>
  <key>EnvironmentVariables</key><dict>
    <key>PERSONA_HOME</key><string>` + x(filepath.Dir(logPath)) + `</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>5</integer>
  <key>StandardOutPath</key><string>/dev/null</string>
  <key>StandardErrorPath</key><string>` + x(logPath) + `</string>
</dict></plist>
`
}

// installMac installs the resident node when requested, then replaces and starts the connector's own agent. The
// caller has already done any pairing. No Electron process, terminal or legacy menu-bar parent is needed afterwards.
func installMac(e *env, withNode bool, personad string, nodeBinAny bool, invite *link.PairInvite) error {
	text := func(key string, args ...i18n.Args) string {
		return i18n.T(i18n.Terminal(), key, args...)
	}
	var oldPID int
	if st, err := link.ReadStatus(e.configPath()); err == nil && link.Running(e.configPath()) {
		oldPID = st.PID
	}
	if link.Running(e.configPath()) && !fileExists(e.linkPlistPath()) {
		return errors.New("another persona-link is running; stop the old parent app or foreground persona-link run before installing the independent service")
	}
	if withNode {
		if err := lockedNodeCommand(e, true, func() error { return nodeInstall(e, personad, nodeBinAny) }); err != nil {
			fmt.Fprintln(e.stdout, text("link-service.node_install_failed", i18n.Args{"error": err}))
		}
	}
	bin, copied, err := e.installBinary()
	if err != nil {
		return fmt.Errorf("%s", text("link-service.binary_install_failed", i18n.Args{"path": e.binPath(), "error": err}))
	}
	if copied {
		fmt.Fprintln(e.stdout, text("link-service.binary_installed", i18n.Args{"path": bin}))
	}
	file := e.linkPlistPath()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(file, []byte(linkPlist(bin, e.logPath())), 0o644); err != nil {
		return err
	}
	domain := e.linkLaunchdDomain()
	_, _ = e.command("launchctl", "bootout", domain+"/"+linkLabel)
	if out, err := e.command("launchctl", "bootstrap", domain, file); err != nil {
		return fmt.Errorf("%s", text("link-service.bootstrap_failed", i18n.Args{"detail": orErr(out, err)}))
	}
	fmt.Fprintln(e.stdout, text("link-service.installed", i18n.Args{"label": linkLabel, "path": file}))
	return waitStarted(e, e.now(), oldPID, invite)
}

func uninstallMac(e *env) error {
	text := func(key string, args ...i18n.Args) string {
		return i18n.T(i18n.Terminal(), key, args...)
	}
	file := e.linkPlistPath()
	if !fileExists(file) {
		fmt.Fprintln(e.stdout, text("link-service.not_installed", i18n.Args{"label": linkLabel}))
		return nil
	}
	_, _ = e.command("launchctl", "bootout", e.linkLaunchdDomain()+"/"+linkLabel)
	if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintln(e.stdout, text("link-service.uninstalled", i18n.Args{"label": linkLabel}))
	return nil
}

func (e *env) macServiceState() *serviceState {
	s := &serviceState{Manager: "launchd", Unit: linkLabel, File: e.linkPlistPath(), Installed: fileExists(e.linkPlistPath())}
	if s.Installed {
		if _, err := e.command("launchctl", "print", e.linkLaunchdDomain()+"/"+linkLabel); err == nil {
			s.Active = "loaded"
		}
	}
	return s
}
