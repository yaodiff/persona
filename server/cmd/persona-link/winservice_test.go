package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/link"
)

// newWindowsEnv is fakeEnv as a Windows computer: this build is a (fake) console program, schtasks answers through
// tasks, which says whether the task is registered.
func newWindowsEnv(t *testing.T) (*fakeEnv, *bool) {
	t.Helper()
	f := newFakeEnv(t)
	f.goos, f.user = "windows", `PC\me & co`
	self := filepath.Join(t.TempDir(), "persona-link.exe")
	if err := os.WriteFile(self, fakePE(peSubsystemConsole), 0o755); err != nil {
		t.Fatal(err)
	}
	f.exe = func() (string, error) { return self, nil }
	registered := new(bool)
	f.onCommand = func(call string) (string, error) {
		switch {
		case strings.HasPrefix(call, "schtasks /Query"):
			if !*registered {
				return "ERROR: The system cannot find the file specified.", errors.New("exit status 1")
			}
			return `"\Persona Link","N/A","Running"`, nil
		case strings.HasPrefix(call, "schtasks /Create"):
			*registered = true
		case strings.HasPrefix(call, "schtasks /Delete"):
			*registered = false
		}
		return "", nil
	}
	return f, registered
}

func TestTaskXML(t *testing.T) {
	x := taskXML(`PC\me & co`, `C:\Users\me & co\.persona\bin\persona-link-service.exe`, "supervise",
		time.Date(2026, 11, 4, 9, 30, 5, 0, time.Local))
	for _, want := range []string{
		`<LogonTrigger>
      <UserId>PC\me &amp; co</UserId>
    </LogonTrigger>`,
		`<LogonType>InteractiveToken</LogonType>`,
		`<RunLevel>LeastPrivilege</RunLevel>`,
		`<Interval>PT5M</Interval>`,
		`<StartBoundary>2026-11-04T09:30:05</StartBoundary>`,
		`<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>`,
		`<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>`,
		`<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>`,
		`<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>`,
		`<Priority>5</Priority>`,
		`<Command>C:\Users\me &amp; co\.persona\bin\persona-link-service.exe</Command>`,
		`<Arguments>supervise</Arguments>`,
	} {
		if !strings.Contains(x, want) {
			t.Errorf("task lacks %s:\n%s", want, x)
		}
	}
	if strings.Contains(x, "RestartOnFailure") {
		t.Error("RestartOnFailure does not restart a program that exited; the time trigger does")
	}
}

func TestUTF16File(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.xml")
	s := "<Task><Command>C:\\Users\\张三\\.persona\\bin\\persona-link-service.exe</Command></Task>\n"
	if err := writeUTF16(path, s); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if b[0] != 0xff || b[1] != 0xfe {
		t.Fatalf("no byte order mark: % x", b[:2])
	}
	if got, err := readUTF16(path); err != nil || got != s {
		t.Fatalf("%q %v", got, err)
	}
}

func TestInstallOnWindows(t *testing.T) {
	f, registered := newWindowsEnv(t)
	f.pairHere(t)
	node := filepath.Join(t.TempDir(), "personad.exe")
	if err := os.WriteFile(node, []byte("node build"), 0o755); err != nil {
		t.Fatal(err)
	}
	answer := f.onCommand
	f.onCommand = func(call string) (string, error) {
		if strings.HasPrefix(call, "schtasks /Run") {
			f.saveStatus(t, link.Status{State: "connected", Kind: "lan", Address: "https://192.0.2.10:18662", Node: "online", HubName: "家里"}, 42)
		}
		return answer(call)
	}
	if err := install(f.env, []string{"--personad", node}); err != nil {
		t.Fatalf("%v\n%s", err, f.out)
	}
	if !*registered {
		t.Fatal("task not registered")
	}
	bin := filepath.Join(f.home, ".persona", "bin")
	if f.binPath() != filepath.Join(bin, "persona-link.exe") || f.nodeBin() != filepath.Join(bin, "personad.exe") {
		t.Fatal(f.binPath(), f.nodeBin())
	}
	if b, _ := os.ReadFile(f.nodeBin()); string(b) != "node build" {
		t.Fatalf("node %q", b)
	}
	if b, _ := os.ReadFile(f.serviceExePath()); subsystemOf(t, b) != peSubsystemGUI {
		t.Fatal("the service's copy is not console-less")
	}
	x, err := readUTF16(f.taskFile())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<Command>" + f.serviceExePath() + "</Command>", "<Arguments>supervise</Arguments>", `<UserId>PC\me &amp; co</UserId>`} {
		if !strings.Contains(x, want) {
			t.Errorf("task lacks %s", want)
		}
	}
	want := []string{"schtasks /Query /TN Persona Link /FO CSV /NH", "schtasks /Create /TN Persona Link /XML " + f.taskFile() + " /F",
		"schtasks /Run /TN Persona Link"}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ran %q", f.calls)
	}
	out := f.out.String()
	for _, want := range []string{"装好了 " + f.binPath(), "装好了 " + f.nodeBin(), "装好了计划任务「Persona Link」",
		"常驻 node 和 persona-link退出了几秒内重启", "连上了：「家里」"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !nodeStatus(f.env).Installed {
		t.Error("node status says the node is not in the task")
	}
	// Again, as an upgrade: the running task is ended first (its programs cannot be replaced while they run), and
	// install waits for the old persona-link to let go of the run lock.
	release, err := link.LockRun(f.configPath())
	if err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	f.out.Reset()
	f.onCommand = func(call string) (string, error) {
		switch {
		case strings.HasPrefix(call, "schtasks /End"):
			release()
		case strings.HasPrefix(call, "schtasks /Run"):
			f.saveStatus(t, link.Status{State: "connected", Node: "online"}, 43)
		}
		return answer(call)
	}
	if err := install(f.env, []string{"--no-node"}); err != nil {
		t.Fatalf("%v\n%s", err, f.out)
	}
	if len(f.calls) < 2 || f.calls[1] != "schtasks /End /TN Persona Link" {
		t.Fatalf("ran %q", f.calls)
	}
	if strings.Contains(f.out.String(), "还有一个 persona-link 在运行") {
		t.Fatalf("output:\n%s", f.out)
	}
	if x, _ := readUTF16(f.taskFile()); !strings.Contains(x, "<Arguments>supervise --no-node</Arguments>") {
		t.Fatal("--no-node not in the task")
	}
	if nodeStatus(f.env).Installed {
		t.Error("node status says the node is in the task")
	}
}

func TestInstallOnWindowsRejectsTheRetiredTrayFlag(t *testing.T) {
	f, _ := newWindowsEnv(t)
	if err := install(f.env, []string{"--no-tray"}); !errors.Is(err, errUsage) {
		t.Fatalf("retired tray flag: %v", err)
	}
	if len(f.calls) != 0 || fileExists(f.taskFile()) {
		t.Fatal("the retired tray flag installed something")
	}
}

func TestInstallOnWindowsWithoutTheNode(t *testing.T) {
	f, _ := newWindowsEnv(t)
	err := install(f.env, nil)
	if x, _ := readUTF16(f.taskFile()); !strings.Contains(x, "<Arguments>supervise --no-node</Arguments>") {
		t.Fatalf("task %s (%v)", x, err)
	}
	out := f.out.String()
	for _, want := range []string{"常驻 node 没装好：没有 " + f.nodeBin(), "--personad", "还没配对"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestUninstallOnWindows(t *testing.T) {
	f, registered := newWindowsEnv(t)
	if err := install(f.env, nil); err != nil {
		t.Fatalf("%v\n%s", err, f.out)
	}
	f.calls = nil
	f.out.Reset()
	if err := uninstall(f.env, nil); err != nil {
		t.Fatal(err)
	}
	if *registered || fileExists(f.taskFile()) || fileExists(f.serviceExePath()) {
		t.Fatalf("left behind: registered %v, %v, %v", *registered, fileExists(f.taskFile()), fileExists(f.serviceExePath()))
	}
	want := []string{"schtasks /Query /TN Persona Link /FO CSV /NH", "schtasks /Query /TN Persona Link /FO CSV /NH",
		"schtasks /End /TN Persona Link", "schtasks /Delete /TN Persona Link /F"}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ran %q", f.calls)
	}
	if out := f.out.String(); !strings.Contains(out, "停掉并删掉了计划任务「Persona Link」") {
		t.Fatalf("output:\n%s", out)
	}
	f.out.Reset()
	if err := uninstall(f.env, nil); err != nil || !strings.Contains(f.out.String(), "没装计划任务") {
		t.Fatalf("%v\n%s", err, f.out)
	}
}

func TestStatusAndNodeOnWindows(t *testing.T) {
	f, registered := newWindowsEnv(t)
	r := gather(f.env)
	var lines []string
	r.serviceText(func(format string, a ...any) { lines = append(lines, format) }, "me")
	if r.Service == nil || r.Service.Manager != "schtasks" || len(lines) != 1 || !strings.Contains(lines[0], "没装") {
		t.Fatalf("%+v %q", r.Service, lines)
	}
	if err := writeUTF16(f.taskFile(), taskXML(f.user, f.serviceExePath(), "supervise", f.now())); err != nil {
		t.Fatal(err)
	}
	*registered = true
	if s := gather(f.env).Service; !s.Installed || s.Active != "Running" {
		t.Fatalf("%+v", s)
	}
	if text := r.text(f.now(), "me"); strings.Contains(text, "托盘") {
		t.Fatalf("the retired tray is still reported:\n%s", text)
	}
	if st := nodeStatus(f.env); !st.Installed || st.Running != "Running" {
		t.Fatalf("%+v", st)
	}
	for _, sub := range []string{"install", "uninstall"} {
		if err := nodeCmd(f.env, []string{sub}); err == nil || !strings.Contains(err.Error(), "同一个计划任务") {
			t.Errorf("node %s: %v", sub, err)
		}
	}
}
