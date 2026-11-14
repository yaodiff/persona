package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// wheel (#7775) measures what the live screen's two-finger scroll does on a Mac: how far one wheel notch scrolls a
// browser page, a Finder window and a terminal; whether notches sent close together go further (macOS accelerating
// them) or merge; and how far a gesture's notches scroll each when replayed at the times the app sends them (-replay,
// schedules from a model of the app's wheel). The page reports its own scroll position; Finder and Terminal are
// measured from the picture, by how far their rows moved.
//
// It uses only windows it opens itself (a browser of its own with a profile of its own, a Finder window on a folder of
// its own, a Terminal window running a script of its own), closes them afterwards and brings back the app that was in
// front. It starts only when nobody has touched the Mac for a minute, and stops as soon as the Mac gets keys, clicks or
// pointer moves it did not send.

type wheelFlags struct {
	apps    *string
	browser *string
	replay  *string
	jsonOut *string
}

func (w *wheelFlags) register(fs *flag.FlagSet) {
	w.apps = fs.String("apps", "page", "comma-separated: page (a browser page), finder, terminal")
	w.browser = fs.String("browser", "", "page: a browser executable to start with a profile of its own (Chrome for Testing or Chromium; Playwright's WebKit)")
	w.replay = fs.String("replay", "", "a JSON file of named notch schedules, {\"name\": \"ms:n,ms:n,…\"}, replayed in each app")
	w.jsonOut = fs.String("json", "", "append one JSON line per measurement to this file")
}

// wheelResult is one measurement: notches sent one way in an app, and how far it scrolled.
type wheelResult struct {
	App     string  `json:"app"`
	Test    string  `json:"test"`
	Notches int     `json:"notches"`
	Seconds float64 `json:"seconds"` // from the first notch to the last
	Pixels  float64 `json:"pixels"`  // the page's CSS pixels, or the window's framebuffer pixels
	Settled float64 `json:"settled"` // seconds from the last notch to the last movement (the page: to within 0.1 s)
	Events  int     `json:"events,omitempty"`
	Deltas  string  `json:"deltas,omitempty"` // the page's wheel deltaY values, and their mode
}

func (r wheelResult) perNotch() float64 {
	if r.Notches == 0 {
		return 0
	}
	return r.Pixels / float64(r.Notches)
}

// The notch tests: one at a time, then runs at shorter and shorter gaps (the app sends a frame's notches back to back).
var (
	wheelAlone = 10
	wheelGaps  = []time.Duration{100 * time.Millisecond, 33 * time.Millisecond, 16 * time.Millisecond, 8 * time.Millisecond, 0}
	wheelRun   = 20
)

func wheelBench(ctx context.Context, t target, cred credentials, f wheelFlags) error {
	// Interrupted, terminated or out of time: it stops between steps and still closes what it opened.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	var schedules map[string][]notchStep
	if *f.replay != "" {
		raw, err := os.ReadFile(*f.replay)
		if err != nil {
			return err
		}
		if schedules, err = parseSchedules(raw); err != nil {
			return err
		}
	}
	idle, err := readInput()
	if err != nil {
		return fmt.Errorf("reading the Mac's input: %w", err)
	}
	if idle.since() < 60 {
		return fmt.Errorf("the Mac had input %.0f s ago: someone may be using it, nothing done", idle.since())
	}
	front, _ := frontApp()
	c, err := open(ctx, t, cred, pf32, appEncodings, "stopwait", nil)
	if err != nil {
		return err
	}
	defer c.Close()
	disp, err := readDisplays(c.Width)
	if err != nil {
		return err
	}
	watch, err := newUserWatch(ctx, c, disp)
	if err != nil {
		return err
	}
	defer func() { // the app that was in front, unless someone is using the Mac now
		if front > 0 && !watch.someone {
			activate(front)
		}
	}()
	var out *os.File
	if *f.jsonOut != "" {
		if out, err = os.OpenFile(*f.jsonOut, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
			return err
		}
		defer out.Close()
	}
	fmt.Printf("| app | test | notches | seconds | settled s | scrolled px | px a notch | wheel events | deltaY |\n")
	fmt.Printf("| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |\n")
	report := func(r wheelResult) {
		events := ""
		if r.App == "page" {
			events = strconv.Itoa(r.Events)
		}
		fmt.Printf("| %s | %s | %d | %.2f | %.2f | %.0f | %.1f | %s | %s |\n", r.App, r.Test, r.Notches, r.Seconds, r.Settled, r.Pixels,
			r.perNotch(), events, r.Deltas)
		if out != nil {
			line, _ := json.Marshal(r)
			_, _ = out.Write(append(line, '\n'))
		}
	}
	for _, app := range strings.Split(*f.apps, ",") {
		var err error
		switch app {
		case "page":
			err = wheelPage(c, watch, *f.browser, schedules, report)
		case "finder", "terminal":
			err = wheelWindow(c, watch, disp, app, schedules, report)
		default:
			err = fmt.Errorf("unknown app %q", app)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", app, err)
		}
	}
	return nil
}

// notchStep is part of a schedule: n notches (> 0 up, as the app counts them) at ms from its start.
type notchStep struct {
	ms float64
	n  int
}

// parseSchedules reads {"name": "ms:n,ms:n,…"}.
func parseSchedules(raw []byte) (map[string][]notchStep, error) {
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	out := map[string][]notchStep{}
	for name, s := range m {
		var steps []notchStep
		for _, part := range strings.Split(s, ",") {
			if part = strings.TrimSpace(part); part == "" {
				continue
			}
			at, n, ok := strings.Cut(part, ":")
			ms, err1 := strconv.ParseFloat(at, 64)
			k, err2 := strconv.Atoi(n)
			if !ok || err1 != nil || err2 != nil {
				return nil, fmt.Errorf("schedule %s: %q is not ms:n", name, part)
			}
			steps = append(steps, notchStep{ms, k})
		}
		sort.SliceStable(steps, func(i, j int) bool { return steps[i].ms < steps[j].ms })
		out[name] = steps
	}
	return out, nil
}

func scheduleNames(s map[string][]notchStep) []string {
	names := make([]string, 0, len(s))
	for k := range s {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// notches sends n wheel notches at p, back to back, as the app does (button 4 up, 5 down, pressed and let go).
func notches(c *probeConn, p image.Point, n int, up bool) {
	b := byte(16)
	if up {
		b = 8
	}
	for range n {
		_ = c.Pointer(p.X, p.Y, b)
		_ = c.Pointer(p.X, p.Y, 0)
	}
}

// wheelProbe sends notches at one place of an app and measures each run of them: reset goes back to where there is
// room either way, before marks where things are just before the first notch, after says how far they went once
// they stopped.
type wheelProbe struct {
	app    string
	up     bool
	at     image.Point
	reset  func() error                                     // back to where there is room
	before func() error                                     // just before the first notch
	after  func(first, last time.Time) (wheelResult, error) // once it has stopped
}

func (w wheelProbe) run(c *probeConn, watch *userWatch, name string, steps []notchStep, report func(wheelResult)) error {
	if err := watch.check(); err != nil {
		return err
	}
	if err := w.reset(); err != nil {
		return err
	}
	if err := w.before(); err != nil {
		return err
	}
	start := time.Now()
	var last time.Time
	total := 0
	for _, s := range steps {
		if d := time.Until(start.Add(time.Duration(s.ms * float64(time.Millisecond)))); d > 0 {
			time.Sleep(d)
		}
		n := s.n
		if n < 0 {
			n = -n
		}
		notches(c, w.at, n, w.up)
		total += n
		last = time.Now()
	}
	r, err := w.after(start, last)
	if err != nil {
		return err
	}
	if r.Settled < 0 {
		r.Settled = 0
	}
	r.App, r.Test, r.Notches = w.app, name, total
	r.Seconds = last.Sub(start).Seconds()
	report(r)
	return watch.check()
}

// tests are the notch tests and then each schedule.
func (w wheelProbe) tests(c *probeConn, watch *userWatch, schedules map[string][]notchStep, report func(wheelResult)) error {
	alone := make([]notchStep, wheelAlone)
	for i := range alone {
		alone[i] = notchStep{float64(i) * 500, 1}
	}
	if err := w.run(c, watch, "one at a time, 500 ms apart", alone, report); err != nil {
		return err
	}
	for _, gap := range wheelGaps {
		run := make([]notchStep, wheelRun)
		for i := range run {
			run[i] = notchStep{float64(i) * float64(gap) / float64(time.Millisecond), 1}
		}
		name := fmt.Sprintf("%d, %d ms apart", wheelRun, gap.Milliseconds())
		if gap == 0 {
			name = fmt.Sprintf("%d at once", wheelRun)
		}
		if err := w.run(c, watch, name, run, report); err != nil {
			return err
		}
	}
	for _, name := range scheduleNames(schedules) {
		if err := w.run(c, watch, name, schedules[name], report); err != nil {
			return err
		}
	}
	return nil
}

// MARK: The browser page

func wheelPage(c *probeConn, watch *userWatch, browser string, schedules map[string][]notchStep, report func(wheelResult)) error {
	if browser == "" {
		return errors.New("-browser: a browser executable to start with a profile of its own")
	}
	// A port of its own: a bench page another run left open (on the fixed port) must not show this run's scenes.
	b, err := newBench("127.0.0.1:0")
	if err != nil {
		return err
	}
	b.set("wheel", false)
	profile, err := os.MkdirTemp("", "vncbench-wheel-browser-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(profile)
	cmd := browserCommand(browser, profile, b.url)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the browser: %w", err)
	}
	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
		}
	}()
	b.front = func() { activate(cmd.Process.Pid) }
	time.Sleep(3 * time.Second)
	b.forward()
	lay, err := ensure(b, c, "wheel")
	if err != nil {
		return err
	}
	c.setLayout(lay)
	// The page found is this run's: moving its scroller changes its marker's colour.
	was := c.markerNow()
	b.mu.Lock()
	b.wseq++
	b.wtop = 1000
	b.mu.Unlock()
	if c.changedAfter(time.Now(), was, 3*time.Second).IsZero() {
		return errors.New("the bench page on the screen is not this run's (an older one?): nothing sent")
	}
	at := lay.at(60, 28+60)
	if err := aim(b, c, at); err != nil {
		return err
	}
	if err := watch.moved(c, at); err != nil {
		return err
	}
	top := func() float64 {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.top
	}
	// still waits until the page's scroller has stayed put for 600 ms (it says where it is ten times a second): where
	// it is, and when it last moved.
	still := func() (float64, time.Time) {
		v, since := top(), time.Now()
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
			time.Sleep(50 * time.Millisecond)
			if n := top(); n != v {
				v, since = n, time.Now()
			} else if time.Since(since) >= 600*time.Millisecond {
				break
			}
		}
		return v, since
	}
	var from float64
	var mark time.Time
	probe := wheelProbe{
		app: "page", at: at,
		reset: func() error {
			b.mu.Lock()
			b.wseq++
			b.wtop = 200000
			b.mu.Unlock()
			if v, _ := still(); math.Abs(v-200000) > 1 {
				return fmt.Errorf("the page's scroller did not go to 200000 (it is at %.0f)", v)
			}
			return aim(b, c, at)
		},
		before: func() error {
			from, mark = top(), time.Now()
			return nil
		},
		after: func(first, last time.Time) (wheelResult, error) {
			to, moved := still()
			r := wheelResult{Pixels: math.Abs(to - from), Settled: moved.Sub(last).Seconds()}
			deltas := map[string]int{}
			for _, e := range b.eventsSince(mark) {
				if e.Kind != "wheel" {
					continue
				}
				r.Events++
				deltas[fmt.Sprintf("%g/%d", e.D, e.M)]++
			}
			r.Deltas = summarize(deltas)
			return r, nil
		},
	}
	return probe.tests(c, watch, schedules, report)
}

// browserCommand starts browser on url with a profile of its own: Chromium-based ones in a window of their own; Playwright's
// WebKit (its MiniBrowser) with its frameworks beside it.
func browserCommand(browser, profile, url string) *exec.Cmd {
	if strings.Contains(browser, "Playwright.app") {
		dir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(browser)))) // …/Playwright.app/Contents/MacOS/Playwright
		cmd := exec.Command(browser, "--user-data-dir="+profile, url)
		cmd.Env = append(os.Environ(), "DYLD_FRAMEWORK_PATH="+dir, "DYLD_LIBRARY_PATH="+dir)
		return cmd
	}
	// --use-mock-keychain: no keychain prompt for the profile's own secrets.
	return exec.Command(browser, "--user-data-dir="+profile, "--use-mock-keychain", "--no-first-run", "--no-default-browser-check",
		"--disable-sync", "--disable-background-networking", "--window-position=80,60", "--window-size=1100,860", "--new-window", url)
}

func summarize(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] || m[keys[i]] == m[keys[j]] && keys[i] < keys[j] })
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s×%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

// MARK: Finder and Terminal

const (
	bundleFinder   = "com.apple.finder"
	bundleTerminal = "com.apple.Terminal"
)

func wheelWindow(c *probeConn, watch *userWatch, disp displays, app string, schedules map[string][]notchStep, report func(wheelResult)) error {
	dir, err := os.MkdirTemp("", "vncbench-wheel-"+app+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	bundle := bundleFinder
	if app == "terminal" {
		bundle = bundleTerminal
	}
	running := appRunning(bundle)
	before, err := listWindows()
	if err != nil {
		return err
	}
	keep := filepath.Join(dir, "keep")
	if app == "finder" {
		if err := fillFolder(dir, 2500); err != nil {
			return err
		}
		if err := exec.Command("open", dir).Run(); err != nil {
			return err
		}
	} else {
		script, err := terminalScript(dir, keep, 6000)
		if err != nil {
			return err
		}
		if err := exec.Command("open", "-a", "Terminal", script).Run(); err != nil {
			return err
		}
	}
	win, err := newWindow(before, bundle, 15*time.Second, app == "terminal" && running)
	if err != nil {
		_ = os.Remove(keep) // a script that did start ends
		if app == "terminal" && !running {
			time.Sleep(time.Second)
			quitApp(bundle)
		}
		return err
	}
	defer closeWindow(c, watch, win, bundle, running, keep)
	time.Sleep(2 * time.Second) // drawn, and the script's lines out
	if win, err = windowNow(win.ID); err != nil {
		return err
	}
	box := disp.toFramebuffer(win.Bounds)
	// The middle of the window: clear of its title bar, toolbar, scroll bars and the terminal's last lines.
	region := image.Rect(box.Min.X+box.Dx()/5, box.Min.Y+box.Dy()/4, box.Max.X-box.Dx()/5, box.Max.Y-box.Dy()/6)
	at := image.Pt((region.Min.X+region.Max.X)/2, (region.Min.Y+region.Max.Y)/2)
	if err := watch.moved(c, at); err != nil {
		return err
	}
	c.settle(400*time.Millisecond, 3*time.Second)
	var track *tracker
	probe := wheelProbe{
		app: app, at: at, up: app == "terminal", // a terminal shows its last lines: there is room only above
		reset: func() error {
			if err := frontmost(win.ID); err != nil {
				return err
			}
			c.settle(400*time.Millisecond, 3*time.Second)
			return nil
		},
		before: func() error {
			track = startTracker(c, box, region)
			return nil
		},
		after: func(first, last time.Time) (wheelResult, error) {
			c.settle(600*time.Millisecond, 10*time.Second)
			moved, at, err := track.stop()
			return wheelResult{Pixels: math.Abs(moved), Settled: at.Sub(last).Seconds()}, err
		},
	}
	return probe.tests(c, watch, schedules, report)
}

var wheelWords = strings.Fields("amber basil cedar delta ember fjord grove harbor island juniper kestrel lantern meadow nectar orchid pebble quarry raven saffron thistle umber velvet willow yarrow zephyr")

// fillFolder puts n empty files with names of different lengths in dir, so no two rows look alike.
func fillFolder(dir string, n int) error {
	r := rand.New(rand.NewSource(7))
	for i := range n {
		name := fmt.Sprintf("%04d", i)
		for range 1 + r.Intn(5) {
			name += " " + wheelWords[r.Intn(len(wheelWords))]
		}
		if err := os.WriteFile(filepath.Join(dir, name+".txt"), nil, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// terminalScript writes a .command that places its window, prints n lines of different lengths and waits while keep
// is there (its shell then ends, and Terminal closes the window).
func terminalScript(dir, keep string, n int) (string, error) {
	r := rand.New(rand.NewSource(11))
	var lines strings.Builder
	for i := range n {
		fmt.Fprintf(&lines, "%05d", i)
		for range 1 + r.Intn(9) {
			lines.WriteString(" " + wheelWords[r.Intn(len(wheelWords))])
		}
		lines.WriteByte('\n')
	}
	text := filepath.Join(dir, "lines.txt")
	if err := os.WriteFile(text, []byte(lines.String()), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(keep, nil, 0o644); err != nil {
		return "", err
	}
	script := filepath.Join(dir, "wheel.command")
	body := fmt.Sprintf("#!/bin/sh\nprintf '\\033[3;80;60t\\033[8;48;120t'\ncat %q\nwhile [ -e %q ]; do sleep 0.3; done\nexit 0\n", text, keep)
	return script, os.WriteFile(script, []byte(body), 0o755)
}

// closeWindow closes the window opened: Terminal's by ending its script, and Terminal itself when it was not running
// before (every window it has is then this one's); Finder's with ⌘W while it is the frontmost window.
func closeWindow(c *probeConn, watch *userWatch, win macWindow, bundle string, wasRunning bool, keep string) {
	defer watch.sent()
	if bundle == bundleTerminal {
		_ = os.Remove(keep)
		time.Sleep(1500 * time.Millisecond)
		if !wasRunning {
			quitApp(bundle)
			return
		}
		if _, err := windowNow(win.ID); err == nil && !watch.someone && frontmost(win.ID) == nil {
			commandKey(c, 'w')
			time.Sleep(time.Second)
		}
	} else if !watch.someone && frontmost(win.ID) == nil {
		commandKey(c, 'w')
		time.Sleep(time.Second)
	}
	if _, err := windowNow(win.ID); err == nil {
		fmt.Fprintf(os.Stderr, "vncbench: the %s window it opened is still there: close it by hand\n", bundle)
	}
}

// commandKey presses ⌘ and key (Screen Sharing takes ⌘ as Super).
func commandKey(c *probeConn, key rune) {
	_ = c.Key(0xffeb, true)
	_ = c.Key(uint32(key), true)
	_ = c.Key(uint32(key), false)
	_ = c.Key(0xffeb, false)
}
