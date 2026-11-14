package hub

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
)

// splitSetup is a systemd setup with <root>/bin/persona-hub beside personad answering `version -v` with build
// (personad answers "persona 1.0 abc").
func splitSetup(t *testing.T, build string) (*setupEnv, *fakeCommands, string) {
	t.Helper()
	cmds := &fakeCommands{replies: map[string]fakeReply{"loginctl show-user": {out: "yes\n"}}}
	e, _ := testSetupEnv(t, setupTestHome(t), setupOptions{}, cmds, "systemctl", "loginctl")
	bin, err := e.hubBinary()
	if err != nil {
		t.Fatal(err)
	}
	hub := filepath.Join(e.root, "bin", "persona-hub")
	if err := os.WriteFile(hub, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmds.replies[bin+" version -v"] = fakeReply{out: "persona 1.0 abc\n"}
	cmds.replies[filepath.Join(e.root, "bin", "personad")+" version -v"] = fakeReply{out: "persona 1.0 abc\n"}
	cmds.replies[realPath(t, hub)+" version -v"] = fakeReply{out: build}
	cmds.replies[hub+" version -v"] = fakeReply{out: build}
	return e, cmds, bin
}

func serviceReport(t *testing.T, e *setupEnv, cfg *Config, bin string) string {
	t.Helper()
	out := e.out.(interface{ String() string })
	before := len(out.String())
	if err := e.systemdService(context.Background(), cfg, bin); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	return out.String()[before:]
}

// The same build beside personad: the unit runs `persona-hub run`. A rerun over a personad unit (an install from
// before) switches it, restarts the hub and says so, the built-in manager it starts included.
func TestSetupServiceRunsPersonaHub(t *testing.T) {
	e, cmds, bin := splitSetup(t, "persona 1.0 abc\n")
	unit := filepath.Join(e.unitDir(), "personad.service")
	if err := os.MkdirAll(e.unitDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unit, []byte(hubServiceUnit(e.root, false, false, false)), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, _ := binaryChanged(bin)
	noteBinary(bin, sum)
	cmds.replies["systemctl --user is-active"] = fakeReply{}
	cmds.replies["systemctl --user is-active --quiet personad.socket"] = fakeReply{err: true}
	t.Setenv("PERSONA_HUB_SOCKET", "0")
	report := serviceReport(t, e, &Config{Listen: ":18662"}, bin)
	b, _ := os.ReadFile(unit)
	if !strings.Contains(string(b), `ExecStart="`+e.root+`/bin/persona-hub" run`+"\n") {
		t.Fatalf("unit:\n%s", b)
	}
	if !cmds.called("systemctl --user restart personad") {
		t.Fatalf("not restarted: %v", cmds.calls)
	}
	if !strings.Contains(report, "服务改成运行 persona-hub，重启了 hub，内置管理者跟着重启") {
		t.Fatalf("report: %s", report)
	}
	// Again: nothing to switch, nothing restarts.
	cmds.calls = nil
	report = serviceReport(t, e, &Config{Listen: ":18662"}, bin)
	if cmds.called("systemctl --user restart") || strings.Contains(report, "改成运行") {
		t.Fatalf("rerun: %v\n%s", cmds.calls, report)
	}
	// The service runs another program than its unit says (an owner's drop-in changed since it started): restarted.
	// runningProgram reads the main process's program from /proc/<pid>/exe, which only Linux has.
	if runtime.GOOS == "linux" {
		cmds.calls = nil
		mainPID := "systemctl --user show -p MainPID --value personad"
		cmds.replies[mainPID] = fakeReply{out: strconv.Itoa(os.Getpid()) + "\n"}
		report = serviceReport(t, e, &Config{Listen: ":18662"}, bin)
		if !cmds.called("systemctl --user restart personad") || !strings.Contains(report, "常驻服务已经装好；服务改成运行 persona-hub，重启了 hub") {
			t.Fatalf("runs another program: %v\n%s", cmds.calls, report)
		}
		delete(cmds.replies, mainPID)
	}
	// Opted out: back to personad, said the same way, and the unit keeps the opt-out; a manager of its own (service
	// mode) is not the hub's child.
	cmds.calls = nil
	e.getenv = func(k string) string {
		if k == "PERSONA_SPLIT_PROGRAMS" {
			return "0"
		}
		return ""
	}
	report = serviceReport(t, e, &Config{Listen: ":18662", Assistant: AssistantProfile{Mode: AssistantModeService}}, bin)
	if b, _ := os.ReadFile(unit); !strings.Contains(string(b), `ExecStart="`+e.root+`/bin/personad" hub`+"\n") ||
		!strings.Contains(string(b), "\nEnvironment=PERSONA_SPLIT_PROGRAMS=0\n") {
		t.Fatalf("unit:\n%s", b)
	}
	if !cmds.called("systemctl --user restart personad") || !strings.Contains(report, "服务改成运行 personad，重启了 hub") || strings.Contains(report, "内置管理者") {
		t.Fatalf("%v\n%s", cmds.calls, report)
	}
	for _, c := range cmds.calls {
		if strings.Contains(c, "version -v") {
			t.Fatalf("opted out, still asked: %q", c)
		}
	}
	// A rerun without it moves the unit back to persona-hub and drops it.
	cmds.calls = nil
	e.getenv = func(string) string { return "" }
	report = serviceReport(t, e, &Config{Listen: ":18662"}, bin)
	if b, _ := os.ReadFile(unit); !strings.Contains(string(b), `ExecStart="`+e.root+`/bin/persona-hub" run`+"\n") || strings.Contains(string(b), "PERSONA_SPLIT_PROGRAMS") {
		t.Fatalf("unit:\n%s", b)
	}
	if !cmds.called("systemctl --user restart personad") || !strings.Contains(report, "服务改成运行 persona-hub，重启了 hub") {
		t.Fatalf("%v\n%s", cmds.calls, report)
	}
}

// Another build beside personad (a half-done upgrade): personad runs the hub, and the report says why.
func TestSetupServiceKeepsPersonadForAnotherBuild(t *testing.T) {
	e, _, bin := splitSetup(t, "persona 0.9 old\n")
	t.Setenv("PERSONA_HUB_SOCKET", "0")
	report := serviceReport(t, e, &Config{Listen: ":18662"}, bin)
	if b, _ := os.ReadFile(filepath.Join(e.unitDir(), "personad.service")); !strings.Contains(string(b), `/bin/personad" hub`+"\n") {
		t.Fatalf("unit:\n%s", b)
	}
	if !strings.Contains(report, "没用 persona-hub（"+filepath.Join(e.root, "bin", "persona-hub")) || !strings.Contains(report, "another build") ||
		!strings.Contains(report, "服务运行 personad") || strings.Contains(report, "改成运行") {
		t.Fatalf("report: %s", report)
	}
}

// hub-systemd.sh's drop-in (a deploy over a unit setup wrote) follows the program; an owner's drop-in setting
// ExecStart is left, and the report says what the service really runs.
func TestSetupServiceDropins(t *testing.T) {
	e, cmds, bin := splitSetup(t, "persona 1.0 abc\n")
	t.Setenv("PERSONA_HUB_SOCKET", "0")
	dir := filepath.Join(e.unitDir(), "personad.service.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	compat := "[Service]\nExecStart=\nExecStart=\"" + e.root + "/bin/personad\" hub\nEnvironment=\"PERSONA_HOME=" + e.root + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, compatDropin), []byte(compat), 0o644); err != nil {
		t.Fatal(err)
	}
	report := serviceReport(t, e, &Config{Listen: ":18662"}, bin)
	want := "[Service]\nExecStart=\nExecStart=\"" + e.root + "/bin/persona-hub\" run\nEnvironment=\"PERSONA_HOME=" + e.root + "\"\n"
	if b, _ := os.ReadFile(filepath.Join(dir, compatDropin)); string(b) != want {
		t.Fatalf("drop-in:\n%s", b)
	}
	if strings.Contains(report, "服务实际运行") {
		t.Fatalf("report: %s", report)
	}

	owner := filepath.Join(dir, "95-mine.conf")
	if err := os.WriteFile(owner, []byte("[Service]\nExecStart=\nExecStart=/opt/mine/personad hub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmds.calls = nil
	report = serviceReport(t, e, &Config{Listen: ":18662"}, bin)
	if b, _ := os.ReadFile(owner); string(b) != "[Service]\nExecStart=\nExecStart=/opt/mine/personad hub\n" {
		t.Fatalf("owner's drop-in changed:\n%s", b)
	}
	if !strings.Contains(report, "服务实际运行 /opt/mine/personad") || !strings.Contains(report, "要它运行 "+filepath.Join(e.root, "bin", "persona-hub")) {
		t.Fatalf("report: %s", report)
	}

	// One that wraps the program (nice, env): the main process is persona-hub, the unit's program the wrapper. Not a
	// switch, no restart for it.
	wrapped := "[Service]\nExecStart=\nExecStart=/usr/bin/nice \"" + e.root + "/bin/persona-hub\" run\n"
	if err := os.WriteFile(owner, []byte(wrapped), 0o644); err != nil {
		t.Fatal(err)
	}
	cmds.replies["systemctl --user is-active"] = fakeReply{}
	cmds.replies["systemctl --user is-active --quiet personad.socket"] = fakeReply{err: true}
	cmds.replies["systemctl --user show -p MainPID --value personad"] = fakeReply{out: strconv.Itoa(os.Getpid()) + "\n"}
	cmds.calls = nil
	report = serviceReport(t, e, &Config{Listen: ":18662"}, bin)
	if cmds.called("systemctl --user restart") || cmds.called("systemctl --user stop") || strings.Contains(report, "改成运行") || !strings.Contains(report, "服务实际运行 /usr/bin/nice") {
		t.Fatalf("wrapped: %v\n%s", cmds.calls, report)
	}
}

// A unit under the former name keeps its program: no persona-hub, not even asked for its build.
func TestSetupServiceFormerNameKeepsPersonad(t *testing.T) {
	e, cmds, _ := splitSetup(t, "persona 1.0 abc\n")
	if err := os.MkdirAll(e.unitDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	service := "[Service]\nExecStart=" + filepath.Join(e.root, "bin", "personad") + " hub\n"
	unit := filepath.Join(e.unitDir(), compat.HubService+".service")
	if err := os.WriteFile(unit, []byte(service), 0o644); err != nil {
		t.Fatal(err)
	}
	bin, err := e.hubBinary()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PERSONA_HUB_SOCKET", "0")
	serviceReport(t, e, &Config{Listen: ":18662"}, bin)
	if b, _ := os.ReadFile(unit); string(b) != service {
		t.Fatalf("unit:\n%s", b)
	}
	for _, c := range cmds.calls {
		if strings.Contains(c, "persona-hub") {
			t.Fatalf("former name, persona-hub asked: %q", c)
		}
	}
}

// macSetup is splitSetup on a Mac, launchd answering nothing loaded; setup runs launchdService and returns its report,
// after writing plist to the agent's file first when it is not "".
func macSetup(t *testing.T, build string) (e *setupEnv, cmds *fakeCommands, bin string, setup func(plist string) string) {
	t.Helper()
	e, cmds, bin = splitSetup(t, build)
	e.goos, e.uid = "darwin", 501
	cmds.replies["launchctl print"] = fakeReply{err: true}
	file := e.plistPath()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	out := e.out.(interface{ String() string })
	setup = func(plist string) string {
		t.Helper()
		if plist != "" {
			if err := os.WriteFile(file, []byte(plist), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		before := len(out.String())
		if err := e.launchdService(context.Background(), &Config{Listen: ":1"}, bin); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		return strings.TrimSpace(out.String()[before:])
	}
	return e, cmds, bin, setup
}

func agentText(t *testing.T, e *setupEnv) string {
	t.Helper()
	b, err := os.ReadFile(e.plistPath())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// On a Mac as on Linux: a new agent runs `persona-hub run` when it is personad's build, and a rerun leaves it.
// PERSONA_SPLIT_PROGRAMS=0 moves it to `personad hub` and stays in the agent; a rerun without it moves it back.
func TestSetupMacAgentRunsPersonaHub(t *testing.T) {
	e, cmds, bin, setup := macSetup(t, "persona 1.0 abc\n")
	hub := filepath.Join(e.root, "bin", "persona-hub")
	report := setup("")
	if got, want := agentText(t, e), hubPlist(e.home, e.root, bin, hub, false); got != want {
		t.Fatalf("agent:\n%s\nwant:\n%s", got, want)
	}
	if !strings.Contains(agentText(t, e), "<string>"+hub+"</string><string>run</string>") || strings.Contains(report, "照旧运行") {
		t.Fatalf("report: %s", report)
	}
	delete(cmds.replies, "launchctl print")
	cmds.calls = nil
	if report := setup(""); !strings.Contains(report, "没动它（launchd "+hubLaunchdLabel+"）") || cmds.called("launchctl bootstrap") {
		t.Fatalf("again: %v\n%s", cmds.calls, report)
	}

	e.getenv = func(k string) string {
		if k == "PERSONA_SPLIT_PROGRAMS" {
			return "0"
		}
		return ""
	}
	cmds.calls = nil
	report = setup("")
	optedOut := hubPlist(e.home, e.root, bin, "", true)
	if agentText(t, e) != optedOut || !strings.Contains(optedOut, "<key>PERSONA_SPLIT_PROGRAMS</key><string>0</string>") ||
		!strings.Contains(report, "服务改成运行 personad，重启了 hub") || !strings.Contains(report, macSwitchNote(i18n.ZhHans, bin)) {
		t.Fatalf("opted out:\n%s\n%s", agentText(t, e), report)
	}
	for _, c := range cmds.calls {
		if strings.Contains(c, "version -v") {
			t.Fatalf("opted out, still asked: %q", c)
		}
	}
	e.getenv = func(string) string { return "" }
	report = setup("")
	if agentText(t, e) != hubPlist(e.home, e.root, bin, hub, false) || !strings.Contains(report, "服务改成运行 persona-hub，重启了 hub") ||
		!strings.Contains(report, "完全磁盘访问权限现在看 persona-hub") {
		t.Fatalf("without it again:\n%s\n%s", agentText(t, e), report)
	}
}

// Another build beside personad (a half-done upgrade): a new agent runs `personad hub`, and the report says why.
func TestSetupMacAgentKeepsPersonadForAnotherBuild(t *testing.T) {
	e, _, bin, setup := macSetup(t, "persona 0.9 old\n")
	report := setup("")
	if agentText(t, e) != hubPlist(e.home, e.root, bin, "", false) || !strings.Contains(report, "没用 persona-hub（") ||
		!strings.Contains(report, "another build") {
		t.Fatalf("%s\n%s", agentText(t, e), report)
	}
}

// An installed Mac: an agent that ran personad stays on it with a persona-hub of its build beside it, and the report
// says why; no program is asked for its build. An agent of someone's own that runs this persona-hub is still the
// hub's, left as it is.
func TestSetupMacInstalledAgentStaysOnPersonad(t *testing.T) {
	e, cmds, bin, setup := macSetup(t, "persona 1.0 abc\n")
	personad := hubPlist(e.home, e.root, bin, "", false)
	if report := setup(personad); !strings.HasSuffix(report, macStaysNote(i18n.ZhHans)) || strings.Contains(report, "改成运行") ||
		strings.Contains(report, "现在看") || agentText(t, e) != personad {
		t.Fatalf("report: %s", report)
	}
	for _, c := range cmds.calls {
		if strings.Contains(c, "version -v") {
			t.Fatalf("installed Mac, asked: %q", c)
		}
	}
	delete(cmds.replies, "launchctl print")
	cmds.calls = nil
	if report := setup(""); !strings.HasSuffix(report, "没动它（launchd "+hubLaunchdLabel+"）"+macStaysNote(i18n.ZhHans)) || cmds.called("launchctl kickstart") {
		t.Fatalf("as it was: %v\n%s", cmds.calls, report)
	}

	file := e.plistPath()
	runsHub := hubPlist(e.home, e.root, bin, filepath.Join(e.root, "bin", "persona-hub"), false)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(filepath.Dir(file), "org.example.hub.plist")
	if err := os.WriteFile(mine, []byte(strings.Replace(runsHub, hubLaunchdLabel, "org.example.hub", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := e.otherHubAgent(bin); got != "org.example.hub" {
		t.Fatalf("other agent %q", got)
	}
	if err := os.Remove(mine); err != nil {
		t.Fatal(err)
	}

	// Without a persona-hub there is nothing to say.
	if err := os.Remove(filepath.Join(e.root, "bin", "persona-hub")); err != nil {
		t.Fatal(err)
	}
	if report := setup(personad); strings.Contains(report, "照旧运行") {
		t.Fatalf("no persona-hub: %s", report)
	}
}
