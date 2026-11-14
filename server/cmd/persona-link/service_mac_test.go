package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/link"
)

func TestMacLinkPlist(t *testing.T) {
	plist := linkPlist(`/Applications/Persona Link.app/Contents/Helpers/persona-link`, `/Users/me/Library/Application Support/Persona/link.log`)
	for _, want := range []string{
		`<key>Label</key><string>com.persona.link</string>`,
		`<string>/Applications/Persona Link.app/Contents/Helpers/persona-link</string><string>run</string><string>--log</string>`,
		`<key>PERSONA_HOME</key><string>/Users/me/Library/Application Support/Persona</string>`,
		`<key>RunAtLoad</key><true/>`, `<key>KeepAlive</key><true/>`,
		`<key>StandardOutPath</key><string>/dev/null</string>`,
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist lacks %s:\n%s", want, plist)
		}
	}
}

func TestInstallOnMacUsesAnIndependentLaunchdService(t *testing.T) {
	f := newFakeEnv(t)
	f.goos = "darwin"
	f.pairHere(t)
	f.onCommand = func(call string) (string, error) {
		if strings.HasPrefix(call, "launchctl bootstrap") {
			f.saveStatus(t, link.Status{State: "connected", Kind: "lan", Address: "https://192.0.2.10:18662", Node: "online", HubName: "Studio"}, 42)
		}
		return "", nil
	}
	if err := install(f.env, []string{"--no-node"}); err != nil {
		t.Fatalf("%v\n%s", err, f.out)
	}
	if !fileExists(f.binPath()) || !fileExists(f.linkPlistPath()) {
		t.Fatal("the fixed desktop executable or its launchd agent is missing")
	}
	b, err := os.ReadFile(f.linkPlistPath())
	if err != nil || !strings.Contains(string(b), f.binPath()) || strings.Contains(string(b), "--parent") {
		t.Fatalf("agent %s (%v)", b, err)
	}
	for _, call := range []string{"launchctl bootout gui/1000/com.persona.link", "launchctl bootstrap gui/1000 " + f.linkPlistPath()} {
		if !strings.Contains(strings.Join(f.calls, "\n"), call) {
			t.Errorf("did not run %q: %q", call, f.calls)
		}
	}
	r := gather(f.env)
	if r.Service == nil || r.Service.Manager != "launchd" || !r.Service.Installed || r.Service.Active != "loaded" {
		t.Fatalf("service %+v", r.Service)
	}
	if out := r.text(f.now(), "me"); !strings.Contains(out, "后台服务：launchd 用户代理 com.persona.link · loaded") {
		t.Fatalf("status:\n%s", out)
	}
	f.calls = nil
	if err := uninstall(f.env, nil); err != nil {
		t.Fatal(err)
	}
	if fileExists(f.linkPlistPath()) || !fileExists(f.configPath()) || !fileExists(f.binPath()) {
		t.Fatal("uninstall must keep pairing and the program unless --purge is explicit")
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), "launchctl bootout gui/1000/com.persona.link") {
		t.Fatalf("calls %q", f.calls)
	}
}

func TestInstallOnMacDoesNotFightAnUnmanagedConnector(t *testing.T) {
	f := newFakeEnv(t)
	f.goos = "darwin"
	release, err := link.LockRun(f.configPath())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := install(f.env, []string{"--no-node"}); err == nil || !strings.Contains(err.Error(), "another persona-link is running") {
		t.Fatalf("unmanaged running connector: %v", err)
	}
	if fileExists(f.linkPlistPath()) || len(f.calls) != 0 {
		t.Fatal("the existing connector was replaced")
	}
}

func TestMacServiceStatusIsReadOnly(t *testing.T) {
	f := newFakeEnv(t)
	f.goos = "darwin"
	if s := gather(f.env).Service; s == nil || s.Installed || s.Active != "" {
		t.Fatalf("before install: %+v", s)
	}
	if len(f.calls) != 0 {
		t.Fatalf("status launched a service: %q", f.calls)
	}
	if err := os.MkdirAll(filepath.Dir(f.linkPlistPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.linkPlistPath(), []byte("agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.onCommand = func(string) (string, error) { return "", errors.New("not loaded") }
	s := gather(f.env)
	if !s.Service.Installed || s.Service.Active != "" || !strings.Contains(s.text(f.now(), "me"), "已装但未加载") {
		t.Fatalf("loaded status: %+v\n%s", s.Service, s.text(f.now(), "me"))
	}
	var raw map[string]any
	var out strings.Builder
	f.stdout = &out
	_ = status(f.env, []string{"--json"})
	if err := json.Unmarshal([]byte(out.String()), &raw); err != nil {
		t.Fatal(err)
	}
	if _, present := raw["tray"]; present {
		t.Fatal("the retired Windows tray is not a status field")
	}
}
