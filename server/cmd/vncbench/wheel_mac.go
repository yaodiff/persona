package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// MARK: The Mac (JavaScript for Automation reading CoreGraphics and AppKit: no Apple events, no permission asked)

func jxa(script string, args ...string) ([]byte, error) {
	out, err := exec.Command("osascript", append([]string{"-l", "JavaScript", "-e", script}, args...)...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
	}
	return out, err
}

// displays maps the Mac's global screen points (top left of the main display at 0, 0) to the framebuffer: Screen
// Sharing sends every display in one framebuffer, laid out as they are arranged.
type displays struct {
	Origin image.Point // the top left corner of all of them, in points
	Scale  float64     // framebuffer pixels a point
}

func (d displays) toFramebuffer(r image.Rectangle) image.Rectangle {
	f := func(p image.Point) image.Point {
		return image.Pt(int(math.Round(float64(p.X-d.Origin.X)*d.Scale)), int(math.Round(float64(p.Y-d.Origin.Y)*d.Scale)))
	}
	return image.Rectangle{f(r.Min), f(r.Max)}
}

func (d displays) toPoints(p image.Point) (float64, float64) {
	return float64(p.X)/d.Scale + float64(d.Origin.X), float64(p.Y)/d.Scale + float64(d.Origin.Y)
}

func readDisplays(fbWidth int) (displays, error) {
	out, err := jxa(`ObjC.import('AppKit');
function run(){ const s=ObjC.unwrap($.NSScreen.screens); const main=s[0].frame;
  return JSON.stringify(s.map(x=>{ const f=x.frame; return [f.origin.x, main.size.height-(f.origin.y+f.size.height), f.size.width, f.size.height]; })); }`)
	if err != nil {
		return displays{}, err
	}
	var frames [][4]float64
	if err := json.Unmarshal(out, &frames); err != nil || len(frames) == 0 {
		return displays{}, fmt.Errorf("the displays: %q", out)
	}
	minX, minY, maxX := math.Inf(1), math.Inf(1), math.Inf(-1)
	for _, f := range frames {
		minX, minY, maxX = math.Min(minX, f[0]), math.Min(minY, f[1]), math.Max(maxX, f[0]+f[2])
	}
	return displays{Origin: image.Pt(int(minX), int(minY)), Scale: float64(fbWidth) / (maxX - minX)}, nil
}

type macWindow struct {
	ID     int             `json:"id"`
	PID    int             `json:"pid"`
	Bundle string          `json:"bundle"`
	Layer  int             `json:"layer"`
	Bounds image.Rectangle `json:"-"`
	B      [4]float64      `json:"b"`
}

// listWindows is the windows on the screen, front to back.
func listWindows() ([]macWindow, error) {
	out, err := jxa(`ObjC.import('CoreGraphics'); ObjC.import('AppKit');
function run(){ const l=ObjC.deepUnwrap(ObjC.castRefToObject($.CGWindowListCopyWindowInfo($.kCGWindowListOptionOnScreenOnly|$.kCGWindowListExcludeDesktopElements,0)));
  const ids={}; return JSON.stringify(l.map(w=>{ const pid=w.kCGWindowOwnerPID;
    if(!(pid in ids)){ const a=$.NSRunningApplication.runningApplicationWithProcessIdentifier(pid); ids[pid]=a.isNil()?'':ObjC.unwrap(a.bundleIdentifier)||''; }
    const b=w.kCGWindowBounds; return {id:w.kCGWindowNumber,pid,bundle:ids[pid],layer:w.kCGWindowLayer,b:[b.X,b.Y,b.Width,b.Height]}; })); }`)
	if err != nil {
		return nil, err
	}
	var ws []macWindow
	if err := json.Unmarshal(out, &ws); err != nil {
		return nil, fmt.Errorf("the windows: %w", err)
	}
	for i := range ws {
		b := ws[i].B
		ws[i].Bounds = image.Rect(int(b[0]), int(b[1]), int(b[0]+b[2]), int(b[1]+b[3]))
	}
	return ws, nil
}

// newWindow waits for a window of bundle that was not there before (placed: the one terminalScript's escape moved
// to 80, 60, not one of someone's; when Terminal was not running, every window it has is this run's).
func newWindow(before []macWindow, bundle string, d time.Duration, placed bool) (macWindow, error) {
	old := map[int]bool{}
	for _, w := range before {
		old[w.ID] = true
	}
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		ws, err := listWindows()
		if err != nil {
			return macWindow{}, err
		}
		for _, w := range ws {
			if w.Bundle == bundle && w.Layer == 0 && !old[w.ID] && w.Bounds.Dx() > 200 && w.Bounds.Dy() > 200 &&
				(!placed || abs(w.Bounds.Min.X-80) <= 4 && abs(w.Bounds.Min.Y-60) <= 4) {
				return w, nil
			}
		}
	}
	return macWindow{}, fmt.Errorf("no new %s window", bundle)
}

func windowNow(id int) (macWindow, error) {
	ws, err := listWindows()
	if err != nil {
		return macWindow{}, err
	}
	for _, w := range ws {
		if w.ID == id {
			return w, nil
		}
	}
	return macWindow{}, fmt.Errorf("window %d is gone", id)
}

// frontmost makes sure window id is the frontmost window, bringing its app forward once if it is not.
func frontmost(id int) error {
	for try := 0; try < 2; try++ {
		ws, err := listWindows()
		if err != nil {
			return err
		}
		for _, w := range ws {
			if w.Layer != 0 {
				continue
			}
			if w.ID == id {
				return nil
			}
			if try == 0 {
				if me, err := windowNow(id); err == nil {
					activate(me.PID)
					time.Sleep(700 * time.Millisecond)
				}
			}
			break
		}
	}
	return fmt.Errorf("window %d is not in front", id)
}

func appRunning(bundle string) bool {
	out, err := jxa(`ObjC.import('AppKit'); function run(a){ return String(ObjC.unwrap($.NSRunningApplication.runningApplicationsWithBundleIdentifier(a[0])).length); }`, bundle)
	return err == nil && strings.TrimSpace(string(out)) != "0"
}

// quitApp ends every instance of bundle (one this started: nothing of anyone's is in it), by a signal: asking it to
// quit would be an Apple event, which macOS may ask the user about.
func quitApp(bundle string) {
	out, err := jxa(`ObjC.import('AppKit'); function run(a){ return ObjC.unwrap($.NSRunningApplication.runningApplicationsWithBundleIdentifier(a[0])).map(x=>x.processIdentifier).join(' '); }`, bundle)
	if err != nil {
		return
	}
	for _, pid := range strings.Fields(string(out)) {
		_ = exec.Command("kill", "-TERM", pid).Run()
	}
}

func frontApp() (int, error) {
	out, err := jxa(`ObjC.import('AppKit'); function run(){ return String($.NSWorkspace.sharedWorkspace.frontmostApplication.processIdentifier); }`)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

func activate(pid int) {
	_, _ = jxa(`ObjC.import('AppKit'); function run(a){ const x=$.NSRunningApplication.runningApplicationWithProcessIdentifier(Number(a[0])); if(!x.isNil()) x.activateWithOptions($.NSApplicationActivateAllWindows); }`, strconv.Itoa(pid))
}

// MARK: Someone using the Mac

// input is how long ago the Mac last got each kind of input (seconds, CoreGraphics' HID system state), and where the
// pointer is (global points).
type input struct {
	Key, Down, Moved, Wheel float64
	X, Y                    float64
}

func (i input) since() float64 { return math.Min(math.Min(i.Key, i.Down), math.Min(i.Moved, i.Wheel)) }

func readInput() (input, error) {
	out, err := jxa(`ObjC.import('CoreGraphics');
function run(){ const s=$.kCGEventSourceStateHIDSystemState, f=t=>$.CGEventSourceSecondsSinceLastEventType(s,t);
  const p=$.CGEventGetLocation($.CGEventCreate(null));
  return JSON.stringify({Key:f(10),Down:Math.min(f(1),f(3),f(25)),Moved:Math.min(f(5),f(6),f(7)),Wheel:f(22),X:p.x,Y:p.y}); }`)
	if err != nil {
		return input{}, err
	}
	var in input
	return in, json.Unmarshal(out, &in)
}

// userWatch tells input someone made from what this sends: it sends no keys or clicks while measuring, so any is
// someone's; pointer moves count too when Screen Sharing's own do not show in the HID state (found out at the start),
// else the pointer being anywhere but where this put it. (Wheel turns are not told apart: this sends them.)
type userWatch struct {
	ctx       context.Context // done: stop
	someone   bool            // seen: from then on nothing more is sent and no app is brought forward
	disp      displays
	checked   time.Time
	ownCounts bool
	at        image.Point // where this put the pointer, framebuffer pixels
	placed    bool
}

func newUserWatch(ctx context.Context, c *probeConn, disp displays) (*userWatch, error) {
	in, err := readInput()
	if err != nil {
		return nil, err
	}
	// Screen Sharing's pointer at the pointer's own place: nothing moves; does it show as input?
	p := image.Pt(int(math.Round((in.X-float64(disp.Origin.X))*disp.Scale)), int(math.Round((in.Y-float64(disp.Origin.Y))*disp.Scale)))
	_ = c.Pointer(p.X, p.Y, 0)
	time.Sleep(500 * time.Millisecond)
	after, err := readInput()
	if err != nil {
		return nil, err
	}
	w := &userWatch{ctx: ctx, disp: disp, checked: time.Now(), ownCounts: after.Moved < 0.5, at: p, placed: true}
	fmt.Fprintf(os.Stderr, "vncbench: Screen Sharing's own pointer moves %s in the HID state\n", map[bool]string{true: "show", false: "do not show"}[w.ownCounts])
	return w, nil
}

// moved records that this put the pointer at p.
func (w *userWatch) moved(c *probeConn, p image.Point) error {
	if err := w.check(); err != nil {
		return err
	}
	_ = c.Pointer(p.X, p.Y, 0)
	w.at, w.placed = p, true
	time.Sleep(200 * time.Millisecond)
	w.checked = time.Now()
	return nil
}

// sent says this just sent keys of its own (⌘W): they are not someone's.
func (w *userWatch) sent() { w.checked = time.Now() }

var errSomeone = errors.New("someone is using the Mac (input this did not send): stopped")

// check fails when the Mac got input since the last check that this did not send.
func (w *userWatch) check() error {
	if w.someone {
		return errSomeone
	}
	if err := w.ctx.Err(); err != nil {
		return fmt.Errorf("stopped: %w", err)
	}
	in, err := readInput()
	if err != nil {
		return err
	}
	since := time.Since(w.checked).Seconds()
	w.checked = time.Now()
	x, y := w.disp.toPoints(w.at)
	w.someone = in.Key < since || in.Down < since || !w.ownCounts && in.Moved < since ||
		w.placed && math.Hypot(in.X-x, in.Y-y) > 3
	if w.someone {
		return errSomeone
	}
	return nil
}
