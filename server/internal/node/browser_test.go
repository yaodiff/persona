package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fakeEgoTarget(t *testing.T, current, app string) {
	t.Helper()
	oldOS, oldVersion, oldApp := installOS, macOSProductVersion, egoApp
	installOS = "darwin"
	macOSProductVersion = func(context.Context) string { return current }
	egoApp = func() string { return app }
	t.Cleanup(func() { installOS, macOSProductVersion, egoApp = oldOS, oldVersion, oldApp })
}

func fakeEgoBundle(t *testing.T, required string) string {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("requires macOS plutil")
	}
	app := filepath.Join(t.TempDir(), "ego lite.app")
	contents := filepath.Join(app, "Contents")
	if err := os.MkdirAll(contents, 0o700); err != nil {
		t.Fatal(err)
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleShortVersionString</key><string>1.2.3</string>
<key>LSMinimumSystemVersion</key><string>%s</string>
</dict></plist>`, required)
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestVersionLess(t *testing.T) {
	for _, tc := range []struct {
		current, required string
		want              bool
	}{
		{"12.7.4", "13.0", true},
		{"13.0", "13.0", false},
		{"12.10", "12.7.4", false},
		{"12.7.4", "12.10", true},
		{"13", "13.0.0", false},
		{"13", "13.0.1", true},
		{"14.0 ", "13.0", false},
		{"", "13.0", false},
		{"12.7.4", "", false},
		{"12.beta", "13.0", false},
		{"12.7.4", "13.0.beta", false},
		{"12..4", "13.0", false},
	} {
		if got := versionLess(tc.current, tc.required); got != tc.want {
			t.Errorf("versionLess(%q, %q) = %v, want %v", tc.current, tc.required, got, tc.want)
		}
	}
}

func TestEgoUnsupportedOS(t *testing.T) {
	for _, tc := range []struct {
		name, current string
		unsupported   bool
	}{
		{"older", "12.7.4", true},
		{"at minimum", "13.0", false},
		{"unknown", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeEgoTarget(t, tc.current, "")
			if cur, req, bad := egoUnsupportedOS(t.Context(), ""); cur != tc.current || req != "13.0" || bad != tc.unsupported {
				t.Fatalf("pre-install minimum: current %q, required %q, unsupported %v", cur, req, bad)
			}
		})
	}
	t.Run("non macOS", func(t *testing.T) {
		fakeEgoTarget(t, "12.7.4", "")
		installOS = "linux"
		macOSProductVersion = func(context.Context) string {
			t.Fatal("read the product version off macOS")
			return ""
		}
		if _, _, bad := egoUnsupportedOS(t.Context(), ""); bad {
			t.Fatal("applied the macOS check off macOS")
		}
	})
	t.Run("installed bundle minimum", func(t *testing.T) {
		app := fakeEgoBundle(t, "12.7.4")
		fakeEgoTarget(t, "12.10", app)
		if cur, req, bad := egoUnsupportedOS(t.Context(), app); cur != "12.10" || req != "12.7.4" || bad {
			t.Fatalf("bundle minimum: current %q, required %q, unsupported %v", cur, req, bad)
		}
	})
}

func TestBrowserStatusReportsUnsupportedOSBeforeProbing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup bool
	}{{"before setup", false}, {"after setup", true}} {
		t.Run(tc.name, func(t *testing.T) {
			fakeEgoHome(t, tc.setup, false, false, "#!/bin/sh\nprintf 'ready 1\\n'\n")
			fakeEgoTarget(t, "12.7.4", t.TempDir())
			egoProbe.mu.Lock()
			at, ok, note := egoProbe.at, egoProbe.ok, egoProbe.note
			egoProbe.at = time.Time{}
			egoProbe.mu.Unlock()
			t.Cleanup(func() {
				egoProbe.mu.Lock()
				egoProbe.at, egoProbe.ok, egoProbe.note = at, ok, note
				egoProbe.mu.Unlock()
			})
			b := browserStatus(t.Context())
			const want = "当前 macOS 12.7.4 低于 ego lite 要求的 13.0，无法运行"
			if !b.Installed || b.SetUp != tc.setup || b.Ready || b.Note != want {
				t.Fatalf("browser status = %+v", b)
			}
			egoProbe.mu.Lock()
			probed := !egoProbe.at.IsZero()
			egoProbe.mu.Unlock()
			if probed {
				t.Fatal("probed an unsupported browser")
			}
		})
	}
}

func TestOpenEgoChecksBundleMinimum(t *testing.T) {
	app := fakeEgoBundle(t, "14.1")
	fakeEgoTarget(t, "14.0", app)
	err := openEgo(t.Context(), func(step string) { t.Fatalf("reached open step %q", step) })
	if err == nil || err.Error() != "当前 macOS 14.0 低于 ego lite 要求的 14.1，无法运行" {
		t.Fatalf("open = %v", err)
	}
}

func TestInstallEgoChecksOSBeforeDownloadAndCopy(t *testing.T) {
	oldFetch := fetchEgo
	t.Cleanup(func() { fetchEgo = oldFetch })
	t.Run("before download", func(t *testing.T) {
		fakeEgoTarget(t, "12.7.4", "")
		fetchEgo = func(context.Context, func(string)) (string, func(), error) {
			t.Fatal("fetched the DMG on unsupported macOS")
			return "", nil, nil
		}
		err := installEgo(t.Context(), func(step string) { t.Fatalf("reached install step %q", step) })
		if err == nil || err.Error() != "当前 macOS 12.7.4 低于 ego lite 要求的 13.0，无法运行" {
			t.Fatalf("install = %v", err)
		}
	})
	t.Run("mounted app requires newer macOS", func(t *testing.T) {
		app := fakeEgoBundle(t, "14.1")
		fakeEgoTarget(t, "13.0", "")
		cleanups := 0
		fetchEgo = func(context.Context, func(string)) (string, func(), error) {
			return app, func() { cleanups++ }, nil
		}
		err := installEgo(t.Context(), func(step string) { t.Fatalf("reached copy step %q", step) })
		if err == nil || err.Error() != "当前 macOS 13.0 低于 ego lite 要求的 14.1，无法运行" {
			t.Fatalf("install = %v", err)
		}
		if cleanups != 1 {
			t.Fatalf("DMG cleanup ran %d times", cleanups)
		}
	})
}

// fakeEgoHome is a home with ego's first-run setup done (Local State, ego-browser) or not, and the statistics' consent.
func fakeEgoHome(t *testing.T, setUp, imported, stats bool, cli string) string {
	t.Helper()
	home := t.TempDir()
	data := filepath.Join(home, "Library", "Application Support", "Citro Labs", "ego lite")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if setUp {
		n := 0
		if imported {
			n = 1
		}
		state := `{"ego":{"welcome_page_shown":true,"onboarding_imported_browser_data":` + map[bool]string{true: "1", false: "0"}[n == 1] + `}}`
		_ = os.WriteFile(filepath.Join(data, "Local State"), []byte(state), 0o600)
	}
	if stats {
		_ = os.WriteFile(filepath.Join(data, "Consent To Send Stats"), []byte("00000000-0000-4000-8000-000000000000"), 0o600)
	}
	if cli != "" {
		bin := filepath.Join(home, ".local", "bin")
		_ = os.MkdirAll(bin, 0o700)
		_ = os.WriteFile(filepath.Join(bin, "ego-browser"), []byte(cli), 0o755)
	}
	old := egoHome
	egoHome = func() string { return home }
	t.Cleanup(func() { egoHome = old })
	return home
}

func TestEgoSetUpIsReadFromItsLocalState(t *testing.T) {
	fakeEgoHome(t, true, true, true, "#!/bin/sh\n")
	if setUp, imported, analytics := egoSetUp(); !setUp || !imported || analytics != "on" {
		t.Fatal(setUp, imported, analytics)
	}
	fakeEgoHome(t, true, false, false, "")
	if setUp, _, analytics := egoSetUp(); setUp || analytics != "off" {
		t.Fatal("no ego-browser yet: not set up", setUp, analytics)
	}
	fakeEgoHome(t, false, false, false, "#!/bin/sh\n")
	if setUp, _, analytics := egoSetUp(); setUp || analytics != "unknown" {
		t.Fatal("never opened", setUp, analytics)
	}
}

func TestBrowserScriptWrapsTheAssistants(t *testing.T) {
	s := browserScript("persona-1a2b3c4d", "await page.goto(\"https://example.com\");\nreturn 1;", false, false, true, "/tmp/x/page.png", "")
	for _, want := range []string{`taskSpace("persona-1a2b3c4d")`, `task.page("p1")`, "return 1;", `page.screenshot({ path: "/tmp/x/page.png" })`, "__r.error"} {
		if !strings.Contains(s, want) {
			t.Errorf("no %q in\n%s", want, s)
		}
	}
	look := browserScript("persona-1", "", true, true, true, "/tmp/x/page.png", "")
	if !strings.Contains(look, `page.snapshot({ scope: "full_page" })`) || strings.Contains(look, "__r.value") {
		t.Fatal(look)
	}
	viewport := browserScript("persona-1", "", true, false, false, "/tmp/x/page.png", "")
	if !strings.Contains(viewport, `page.snapshot({ scope: "only_within_viewport" })`) {
		t.Fatalf("ego's viewport scope: %s", viewport)
	}
	text, r := splitBrowserOutput("hello\nworld\n" + browserMark + `{"value":2,"url":"https://example.com/"}` + "\n")
	if text != "hello\nworld" || r["value"] != float64(2) || r["url"] != "https://example.com/" {
		t.Fatal(text, r)
	}
	if _, r := splitBrowserOutput("Error: boom"); r != nil {
		t.Fatal("no result line")
	}
	long, _ := json.Marshal(map[string]any{"snapshot": strings.Repeat("界", browserMost)})
	if c := clipJSON(long); len(c) > browserMost+400 || !json.Valid(c) {
		t.Fatalf("%d bytes, valid %v", len(c), json.Valid(c))
	}
}

// browser.call against a fake ego-browser that runs nothing: it answers what the wrapped script would print.
func TestBrowserCall(t *testing.T) {
	cli := `#!/bin/sh
script=$(cat)
case "$script" in *throwme*) echo "TypeError: nope" >&2; exit 1 ;; esac
printf 'log line\n'
printf '@@persona-browser@@{"value":"done","url":"https://example.com/","title":"Example Domain"}\n'
`
	home := fakeEgoHome(t, true, false, false, cli)
	downloads := filepath.Join(home, ".persona", "browser-downloads")
	if got := browserDownloadsDir(); got != downloads {
		t.Errorf("browser downloads directory = %q, want %q", got, downloads)
	}
	if _, err := browserCall(t.Context(), json.RawMessage(`{"session": "persona-1", "look": true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(downloads); !os.IsNotExist(err) {
		t.Fatalf("look call should leave the downloads directory absent: %v", err)
	}
	res, err := browserCall(context.Background(), json.RawMessage(`{"session": "persona-1", "script": "return 'done'"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if json.Unmarshal(res.Output, &out) != nil || out["value"] != "done" || out["console"] != "log line" || out["title"] != "Example Domain" {
		t.Fatalf("%s", res.Output)
	}
	if info, err := os.Stat(downloads); err != nil {
		t.Error(err)
	} else if !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		t.Errorf("browser downloads directory has mode %v, want a private directory", info.Mode())
	}
	if _, err := browserCall(context.Background(), json.RawMessage(`{"session": "persona-1", "script": "throwme"}`)); err == nil || !strings.Contains(err.Error(), "TypeError: nope") {
		t.Fatalf("what it said: %v", err)
	}
	if _, err := browserCall(context.Background(), json.RawMessage(`{"session": "persona-1", "script": "  "}`)); err == nil {
		t.Fatal("an empty script")
	}
	fakeEgoHome(t, false, false, false, "")
	if _, err := browserCall(context.Background(), json.RawMessage(`{"session": "persona-1", "look": true}`)); err == nil {
		t.Fatal("no ego-browser")
	}
}

// The real ego lite on this Mac (PERSONA_EGO_E2E=1, its first-run setup done): the installed app passes the install's
// checks, status says ready, a look at example.com in a task space of its own brings back its snapshot and a
// screenshot, and the space is closed after.
func TestEgoEndToEnd(t *testing.T) {
	if os.Getenv("PERSONA_EGO_E2E") == "" {
		t.Skip("needs ego lite set up on this Mac: PERSONA_EGO_E2E=1")
	}
	ctx := context.Background()
	if app := egoApp(); app == "" {
		t.Fatal("not installed")
	} else if err := verifyEgoApp(ctx, app); err != nil {
		t.Fatal("the installed app fails the install's checks:", err)
	}
	st := browserStatus(ctx)
	t.Logf("status: %+v", st)
	if !st.Ready {
		t.Fatalf("not ready: %s", st.Note)
	}
	const session = "persona-e2e-test"
	defer closeSpace(t, `const t = await taskSpace("`+session+`"); await t.finish({ keep: [] });`)
	if _, err := browserCall(ctx, json.RawMessage(`{"session": "`+session+`", "script": "await page.goto(\"https://example.com\"); return await page.title();", "screenshot": true}`)); err != nil {
		t.Fatal(err)
	}
	res, err := browserCall(ctx, json.RawMessage(`{"session": "`+session+`", "look": true}`))
	if err != nil || !strings.Contains(string(res.Output), "Example Domain") || res.Image == nil {
		t.Fatalf("%v %.300s", err, res.Output)
	}
	t.Logf("look: %d bytes of JSON, screenshot %s %dx%d", len(res.Output), res.Image.Mime, res.Image.Width, res.Image.Height)
}

func closeSpace(t *testing.T, script string) {
	t.Helper()
	if out, err := osexec.Command(egoBin(), "nodejs", "-e", script).CombinedOutput(); err != nil {
		t.Logf("closing the task space: %v %s", err, out)
	}
}

// The real installer (PERSONA_EGO_INSTALLER_CHECK=1, a Mac): ego's DMG for this chip downloads, mounts, and the app in
// it passes the install's checks. Nothing is installed.
func TestEgoInstallerCheck(t *testing.T) {
	if os.Getenv("PERSONA_EGO_INSTALLER_CHECK") == "" {
		t.Skip("downloads ego's DMG: PERSONA_EGO_INSTALLER_CHECK=1")
	}
	var steps []string
	app, done, err := fetchEgo(context.Background(), func(s string) { steps = append(steps, s) })
	if err != nil {
		t.Fatal(steps, err)
	}
	defer done()
	version, _ := osexec.Command("/usr/bin/plutil", "-extract", "CFBundleShortVersionString", "raw", "-o", "-", filepath.Join(app, "Contents", "Info.plist")).Output()
	t.Logf("%v: %s, version %s", steps, filepath.Base(app), strings.TrimSpace(string(version)))
	if len(steps) != 2 {
		t.Fatal(steps)
	}
}

// A script that prints far more than the model gets (a log loop, a page that floods the console) is not kept whole
// while it runs (#3980): the start of what it printed and its result line come back, and the node holds a bounded
// amount whatever the script prints.
func TestBrowserCallKeepsABoundedOutput(t *testing.T) {
	cli := `#!/bin/sh
cat >/dev/null
printf 'first line\n'
yes 'a line the script printed over and over' | head -c 268435456
printf '\n@@persona-browser@@{"value":"done","url":"https://example.com/","title":"Example Domain"}\n'
`
	fakeEgoHome(t, true, false, false, cli)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	res, err := browserCall(context.Background(), json.RawMessage(`{"session": "persona-1", "script": "return 'done'"}`))
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if json.Unmarshal(res.Output, &out) != nil || out["value"] != "done" || out["title"] != "Example Domain" {
		t.Fatalf("%.300s", res.Output)
	}
	if console, _ := out["console"].(string); !strings.HasPrefix(console, "first line\n") {
		t.Fatalf("console = %.100q", console)
	}
	// 256 MB printed; what is kept is at most twice the tail (it slides) and the head.
	if got := after.TotalAlloc - before.TotalAlloc; got > 160<<20 {
		t.Fatalf("256 MB of output cost %d MB of allocations", got>>20)
	}
}

func TestKeptOutput(t *testing.T) {
	k := &keptOutput{headMost: 4, tailMost: 6}
	for _, s := range []string{"ab", "cdef", "ghijklmnopq", "rs", "tuvwxyz"} {
		if n, err := k.Write([]byte(s)); n != len(s) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", s, n, err)
		}
	}
	if got, want := k.String(), "abcd\n… (16 bytes left out)\nuvwxyz"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	// A write that only part of the tail makes way for.
	k = &keptOutput{headMost: 4, tailMost: 6}
	for _, s := range []string{"head", "123456789", "abcd"} {
		_, _ = k.Write([]byte(s))
	}
	if got, want := k.String(), "head\n… (7 bytes left out)\n89abcd"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	small := &keptOutput{headMost: 4, tailMost: 6}
	_, _ = small.Write([]byte("abcdefgh"))
	if got := small.String(); got != "abcdefgh" {
		t.Fatalf("nothing to leave out: %q", got)
	}
}
