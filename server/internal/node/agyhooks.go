package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/nodeclient"
)

// Persona's own Antigravity CLI hooks (docs/integrations/engines.md "Capabilities by engine", #13729), the way Moshi's
// hook does it: two report-only entries in agy's global hooks file (PreInvocation names the conversation a pane is in,
// Stop ends a turn) that tell the node which conversation runs in which terminal. The pane comes from the
// environment the engine gave the process (nodeclient.HookPane), so this works under any engine and without herdr's
// own antigravity-cli integration; the TUI stays the only writer of the conversation, the node only reads its files.
//
// Where the entries live: a top-level group "persona" in <agy config>/hooks.json, the file herdr, Moshi and Orca
// already use for the TUI. The other way, an agy plugin directory (the companion's media guard is one), was not
// chosen: a plugin needs agy to scan its directory and a manifest, the global file is the one agy is known to load
// for every interactive session, and one owned key is as cleanly removable as a directory.

// agyHooksGroup is the group the node owns in agy's hooks.json; every other group is left as it is.
const agyHooksGroup = "persona"

// agyHookTimeout is agy's limit for a Persona entry (seconds); the hook gives up sooner (nodeclient.RunAgyHook).
const agyHookTimeout = 5

// agyHooksOffFile, in the node's directory, says the person removed the entries and the node must not put them back.
const agyHooksOffFile = "agy-hooks.off"

// agyHooksFile is agy's global hooks file: <agy config>/hooks.json beside the data directory
// (~/.gemini/antigravity-cli and ~/.gemini/config by default; the companion's plugins sit there too).
func (n *Node) agyHooksFile() string {
	p := filepath.Join(filepath.Dir(n.agyDataRoot()), "config", "hooks.json")
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real // a dotfiles repo's link: edit the target
	}
	return p
}

// agyInstalled says agy is on this computer: its data directory or config directory is there, or the program is found.
func (n *Node) agyInstalled() bool {
	for _, d := range []string{n.agyDataRoot(), filepath.Dir(n.agyHooksFile())} {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return true
		}
	}
	_, err := exec.LookPath("agy")
	return err == nil
}

// agyHookCommand is the command of one entry: this binary in report-only mode, with the node's directory named when
// it is not the default (agy runs the hook with the terminal's environment, which has no PERSONA_HOME).
func agyHookCommand(bin, dir, home, event string) string {
	cmd := shellQuote(bin) + " hook "
	if dir != "" && dir != identity.DataDir(home) {
		cmd += nodeclient.HomeFlag + " " + shellQuote(dir) + " "
	}
	return cmd + nodeclient.AgyFlag + " " + event
}

// agyHooksWanted is the group's JSON as the file should have it: entries in agy's own key order (command, timeout, type).
func agyHooksWanted(bin, dir, home string) (json.RawMessage, error) {
	type entry struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
		Type    string `json:"type"`
	}
	var g jsonObject
	for _, ev := range nodeclient.AgyEvents {
		g.set(ev, []entry{{Command: agyHookCommand(bin, dir, home, ev), Timeout: agyHookTimeout, Type: "command"}})
	}
	return marshalPlain(g)
}

// installAgyHooks adds Persona's group to the hooks file at path (created when missing) or replaces it when it is
// not what it should be now; returns whether the file changed. Other groups keep their place and value.
func installAgyHooks(path string, want json.RawMessage) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return editSettings(path, true, func(top *jsonObject) (bool, error) {
		if cur, ok := top.get(agyHooksGroup); ok && sameGroups([]json.RawMessage{cur}, []json.RawMessage{want}) {
			return false, nil
		}
		top.set(agyHooksGroup, want)
		return true, nil
	})
}

// uninstallAgyHooks removes Persona's group and returns whether it did; a file without it is not rewritten.
func uninstallAgyHooks(path string) (bool, error) {
	return editSettings(path, false, func(top *jsonObject) (bool, error) {
		if _, ok := top.get(agyHooksGroup); !ok {
			return false, nil
		}
		top.remove(agyHooksGroup)
		return true, nil
	})
}

// agyHooksState is what the doctor reports: "installed", "missing", or "stale" (a group that differs from what this
// node would write now, a binary that moved).
func agyHooksState(path string, want json.RawMessage) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	var top jsonObject
	if err := json.Unmarshal(b, &top); err != nil {
		return "", fmt.Errorf("not a JSON object: %w", err)
	}
	cur, ok := top.get(agyHooksGroup)
	switch {
	case !ok:
		return "missing", nil
	case sameGroups([]json.RawMessage{cur}, []json.RawMessage{want}):
		return "installed", nil
	}
	return "stale", nil
}

func (n *Node) agyHooksOptedOut() bool {
	_, err := os.Stat(filepath.Join(n.dir, agyHooksOffFile))
	return err == nil
}

// ensureAgyHooks puts Persona's entries into agy's hooks file when agy is here and the person has not removed them.
func (n *Node) ensureAgyHooks() {
	if !n.agyInstalled() {
		return
	}
	n.ensureAgyHooksForLaunch()
}

// ensureAgyHooksForLaunch ensures Persona's entries are in agy's hooks file right before Persona starts or resumes an
// agy Agent (#13950). Unlike ensureAgyHooks at node startup, it does not gate on agyInstalled so an agy installed after
// startup or invoked through a custom launcher gets its hooks before launching; explicit opt-out (agyHooksOffFile) and
// scoped/unconfigured nodes are still respected.
func (n *Node) ensureAgyHooksForLaunch() {
	if n.sessionScope != nil || n.dir == "" || n.bin == "" || n.agyHooksOptedOut() {
		return
	}
	want, err := agyHooksWanted(n.bin, n.dir, n.home)
	if err != nil {
		return
	}
	path := n.agyHooksFile()
	switch changed, err := installAgyHooks(path, want); {
	case err != nil:
		n.logf("agy hooks (%s): %v", path, err)
	case changed:
		n.logf("agy hooks installed in %s", path)
	}
}

// AgyHooksCommand is `personad node agy-hooks [status|install|uninstall]`: the entries in agy's hooks file. uninstall
// removes them and keeps the node from putting them back (agyHooksOffFile); install undoes that.
func AgyHooksCommand(args []string, out interface{ Write([]byte) (int, error) }) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := identity.DataDir(home)
	_, bin, err := selfProgram(home, dir)
	if err != nil {
		return err
	}
	n := &Node{home: home, dir: dir, bin: bin}
	path := n.agyHooksFile()
	say := func(format string, a ...any) { fmt.Fprintf(out, format+"\n", a...) }
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	if len(args) > 1 {
		return errors.New("usage: personad node agy-hooks [status|install|uninstall]")
	}
	switch sub {
	case "status":
		want, err := agyHooksWanted(bin, dir, home)
		if err != nil {
			return err
		}
		st, err := agyHooksState(path, want)
		if err != nil {
			return err
		}
		say("agy-hooks: %s (%s)", st, path)
	case "install":
		_ = os.Remove(filepath.Join(dir, agyHooksOffFile))
		want, err := agyHooksWanted(bin, dir, home)
		if err != nil {
			return err
		}
		if _, err := installAgyHooks(path, want); err != nil {
			return err
		}
		say("agy-hooks: installed in %s", path)
	case "uninstall":
		if err := os.WriteFile(filepath.Join(dir, agyHooksOffFile), nil, 0o600); err != nil {
			return err
		}
		changed, err := uninstallAgyHooks(path)
		if err != nil {
			return err
		}
		say("agy-hooks: %s (%s)", map[bool]string{true: "removed", false: "not installed"}[changed], path)
	default:
		return errors.New("usage: personad node agy-hooks [status|install|uninstall]")
	}
	return nil
}

// agyHookReport is what a pane's last agy hook said.
type agyHookReport struct {
	conversation    string
	path            string
	event           string
	at              time.Time
	promptDigest    string
	invocationSteps int
	settled         bool
}

// agyReportBytes bounds one report's input; the fields are a few hundred bytes.
const agyReportBytes = 8 << 10

// pathUnder says path is inside dir (both made real where they exist, as macOS /var and /private/var differ).
func pathUnder(dir, path string) bool {
	real := func(p string) string {
		p = filepath.Clean(p)
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	d, p := real(dir), real(path)
	rel, err := filepath.Rel(d, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// agyHookKind handles an agy.hook request: it records which conversation the pane is in. A report without a pane or a
// conversation id, or whose transcript is outside agy's data directory, is dropped.
func (n *Node) agyHookKind(req sockRequest) map[string]any {
	var rep nodeclient.AgyReport
	if len(req.Input) > agyReportBytes || json.Unmarshal(req.Input, &rep) != nil || req.Pane == "" {
		return map[string]any{"ok": false}
	}
	identity, ok := nodeclient.ParseAgyReport(rep.Event, agyReportInput(rep))
	if !ok || identity.TranscriptPath != "" && !pathUnder(n.agyDataRoot(), identity.TranscriptPath) {
		return map[string]any{"ok": false}
	}
	n.mu.Lock()
	if n.agyReports == nil {
		n.agyReports = map[string]agyHookReport{}
	}
	prev := n.agyReports[req.Pane]
	next := agyHookReport{conversation: identity.Conversation, path: identity.TranscriptPath, event: identity.Event, at: time.Now(),
		promptDigest: rep.PromptDigest}
	if next.event == "PreInvocation" {
		next.invocationSteps = rep.InvocationSteps
	} else if next.conversation == prev.conversation && next.promptDigest != "" && next.promptDigest == prev.promptDigest {
		// Stop carries no step count. Keep the current invocation's position so a later repetition of the same
		// prompt cannot inherit this completion.
		next.invocationSteps, next.settled = prev.invocationSteps, rep.Settled
	}
	n.agyReports[req.Pane] = next
	n.mu.Unlock()
	if prev.conversation != next.conversation || prev.event != next.event || prev.promptDigest != next.promptDigest ||
		prev.invocationSteps != next.invocationSteps || prev.settled != next.settled {
		if sig, ok := n.watcherSignal(host.ParsePane(req.Pane).Kind); ok {
			sig.poke(false)
		}
	}
	return map[string]any{"ok": true}
}

// agyReportInput is a report in the shape of agy's own hook input, so the node parses it with the hook's parser.
func agyReportInput(rep nodeclient.AgyReport) []byte {
	b, _ := json.Marshal(map[string]string{"conversationId": rep.Conversation, "transcriptPath": rep.TranscriptPath})
	return b
}

// agyReportGrace is how long a report may wait for its pane to show up in an engine's list (the hook can be quicker
// than the next snapshot) before it is dropped as the report of a pane that is gone.
const agyReportGrace = 15 * time.Second

// agyRowOfPane is the row of pane in m, if there is one.
func agyRowOfPane(m map[string]host.Agent, pane string) (host.Agent, bool) {
	for _, a := range m {
		if a.PaneID == pane {
			return a, true
		}
	}
	return host.Agent{}, false
}

const agyHooksCheck = "agy-hooks"

// checkAgyHooks is the doctor's line for Persona's entries in agy's hooks file: installed, missing, or stale (they
// run a program that is not this one). Missing is a warning, not a failure: agy panes then show chat history only
// through herdr's own integration. A node that was told to leave them out (agyHooksOffFile) is fine.
func checkAgyHooks(opts DoctorOptions) DoctorCheck {
	c := DoctorCheck{Name: agyHooksCheck, Items: []string{}}
	home, _ := os.UserHomeDir()
	want, err := agyHooksWanted(opts.Bin, opts.Dir, home)
	if err != nil {
		c.Status, c.Detail = DoctorWarn, err.Error()
		return c
	}
	st, err := agyHooksState(opts.AgyHooksFile, want)
	if err != nil {
		c.Status, c.Detail = DoctorWarn, i18n.T(opts.Lang, "node-doctor.agy_hooks_unreadable", i18n.Args{"error": shorten(err.Error(), 60)})
		return c
	}
	switch st {
	case "installed":
		c.Status, c.Detail = DoctorOK, i18n.T(opts.Lang, "node-doctor.agy_hooks_installed")
	case "stale":
		c.Status = DoctorWarn
		c.Detail = i18n.T(opts.Lang, "node-doctor.agy_hooks_stale")
		c.Hint = i18n.T(opts.Lang, "node-doctor.agy_hooks_hint")
	default:
		if _, off := os.Stat(filepath.Join(opts.Dir, agyHooksOffFile)); off == nil {
			c.Status, c.Detail = DoctorOK, i18n.T(opts.Lang, "node-doctor.agy_hooks_off")
			return c
		}
		c.Status = DoctorWarn
		c.Detail = i18n.T(opts.Lang, "node-doctor.agy_hooks_missing")
		c.Hint = i18n.T(opts.Lang, "node-doctor.agy_hooks_hint")
	}
	return c
}

// fixAgyHooks is --fix for the check above: writes the entries again when they are missing or stale, unless the person
// removed them. True when the file changed.
func fixAgyHooks(opts DoctorOptions) bool {
	if opts.AgyHooksFile == "" {
		return false
	}
	if _, off := os.Stat(filepath.Join(opts.Dir, agyHooksOffFile)); off == nil {
		return false
	}
	home, _ := os.UserHomeDir()
	want, err := agyHooksWanted(opts.Bin, opts.Dir, home)
	if err != nil {
		return false
	}
	changed, _ := installAgyHooks(opts.AgyHooksFile, want)
	return changed
}
