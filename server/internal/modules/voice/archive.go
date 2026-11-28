package voice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// A call's archive (#13249): with archive_machine set, everything said in a call (the user's lines, captions and
// typed, and the model's, in the order they were said, each with its time) is written as one Markdown file into
// archive_dir on that computer when the call ends, through the hub's POST /api/v1/machines/{mid}/files/write. The
// extension writes nothing to its own disk: the lines are kept in memory for the call, the hub only relays the
// write, and the log says how many lines went where, never what was said. A computer that is not reachable (a Mac
// asleep) is tried again for archiveRetryFor, the file held in memory meanwhile (at most archivePendingMost of them),
// then dropped (logged). A call still open when the extension stops is ended and written then (a last try). Meeting
// calls and dictation are not archived; a call picked up again on a new socket is archived on its own, marked
// resumed.

// DefaultArchiveDir is where a call's file goes when archive_dir is not set.
const DefaultArchiveDir = "~/PersonaCalls"

const (
	// archiveLinesMost and archiveBytesMost are the most lines and bytes of text one call keeps for its archive (the
	// computer takes a file of at most 4 MiB); later lines are counted, not kept.
	archiveLinesMost = 2000
	archiveBytesMost = 3 << 20
	// archivePendingMost is the most archives held in memory while their computer cannot take them.
	archivePendingMost = 50
	// archiveTryTimeout bounds one try (the folder, then the file).
	archiveTryTimeout = 30 * time.Second
	// archiveFinalWait is how long a stopping extension waits for the archives being written.
	archiveFinalWait = 5 * time.Second
)

var (
	// archiveRetryFor is how long a write that failed is tried again: a night, for a Mac that sleeps.
	archiveRetryFor = 24 * time.Hour
	// archiveRetryWaits are the waits between tries (the last repeats).
	archiveRetryWaits = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute}
)

// keptLine is a finished line of the call's archive.
type keptLine struct {
	role, text string
	at         time.Time
}

// keep adds a finished line to the call's archive (mu held), in the order it was said (as place does for history).
func (c *conversation) keep(role, text string, at time.Time) {
	text = strings.TrimSpace(text)
	if text == "" || c.pl == nil || c.pl.cfg.ArchiveMachine == "" {
		return
	}
	if len(c.kept) >= archiveLinesMost || c.keptBytes+len(text) > archiveBytesMost {
		c.keptDropped++
		return
	}
	c.keptBytes += len(text)
	if at.IsZero() {
		at = time.Now()
	}
	l := keptLine{role: role, text: text, at: at}
	i := len(c.kept)
	if role == "user" {
		for i > 0 && c.kept[i-1].role == "assistant" && c.kept[i-1].at.After(at) {
			i--
		}
	}
	c.kept = append(c.kept, keptLine{})
	copy(c.kept[i+1:], c.kept[i:])
	c.kept[i] = l
}

// callArchive is what one call's file is made of.
type callArchive struct {
	id            int64
	opened, ended time.Time
	resumed       bool
	target, model string
	lines         []keptLine
	dropped       int
	machine, dir  string
	nonce         string // the extension's run (Plugin.nonce): a name taken on the computer is this file's own
}

// archive takes what the call kept for its file (mu held), nil when there is nothing to write.
func (c *conversation) archive() *callArchive {
	if c.pl == nil || c.pl.cfg.ArchiveMachine == "" || c.dictation || c.meeting != nil {
		return nil
	}
	// Lines still open as the call ends (a caption or an answer cut off by the hang-up) are kept as they are.
	if c.user != nil {
		c.keep("user", c.user.text, c.user.at)
	}
	if c.asst != nil {
		c.keep("assistant", c.asst.text, c.asst.at)
	}
	if len(c.kept) == 0 {
		return nil
	}
	a := &callArchive{id: c.log.id, opened: c.log.opened, ended: time.Now(), resumed: c.resumed, model: c.modelID,
		lines: c.kept, dropped: c.keptDropped, machine: c.pl.cfg.ArchiveMachine, nonce: c.pl.nonce,
		dir: strings.TrimRight(firstNonEmpty(c.pl.cfg.ArchiveDir, DefaultArchiveDir), "/")}
	if c.target != nil {
		a.target = c.target.MachineID + "/" + c.target.SessionID
	}
	c.kept, c.keptDropped, c.keptBytes = nil, 0, 0
	return a
}

// name is the file's name: the call's start on this machine's clock, its id and the extension's run, so two calls
// never share one, and a name already taken is this file written by a try whose answer was lost.
func (a *callArchive) name() string {
	return fmt.Sprintf("%s-call%d-%s.md", a.opened.Format("2006-01-02-150405"), a.id, a.nonce)
}

// markdown is the file: a header of facts, then one line per thing said, "- HH:MM:SS [user] …" or "[assistant]".
func (a *callArchive) markdown() string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "call: %d\nstarted: %s\nended: %s\n", a.id, a.opened.Format(time.RFC3339), a.ended.Format(time.RFC3339))
	fmt.Fprintf(&b, "resumed: %t\n", a.resumed)
	if a.target != "" {
		fmt.Fprintf(&b, "conversation: %s\n", a.target)
	}
	if a.model != "" {
		fmt.Fprintf(&b, "model: %s\n", a.model)
	}
	fmt.Fprintf(&b, "lines: %d\n", len(a.lines))
	if a.dropped > 0 {
		fmt.Fprintf(&b, "lines_not_kept: %d\n", a.dropped)
	}
	b.WriteString("---\n\n")
	for _, l := range a.lines {
		text := strings.Join(strings.Fields(strings.ReplaceAll(l.text, "\r", " ")), " ")
		fmt.Fprintf(&b, "- %s [%s] %s\n", l.at.Format("15:04:05"), l.role, text)
	}
	return b.String()
}

// startArchive writes a call's file on its own goroutine (shutdown, once the call has closed), unless too many wait
// for their computer already.
func (pl *Plugin) startArchive(a *callArchive) {
	pl.archMu.Lock()
	if pl.archN >= archivePendingMost {
		pl.archMu.Unlock()
		pl.logf("call %d: archive dropped: %d archives wait for their computer already", a.id, archivePendingMost)
		return
	}
	pl.archN++
	pl.archMu.Unlock()
	go func() {
		defer func() {
			pl.archMu.Lock()
			pl.archN--
			pl.archMu.Unlock()
		}()
		pl.writeArchive(pl.stop, a)
	}()
}

// waitArchives waits at most d for the archives being written (a stopping extension's last tries).
func (pl *Plugin) waitArchives(d time.Duration) {
	for deadline := time.Now().Add(d); ; time.Sleep(20 * time.Millisecond) {
		pl.archMu.Lock()
		n := pl.archN
		pl.archMu.Unlock()
		if n == 0 || time.Now().After(deadline) {
			return
		}
	}
}

// writeArchive writes a call's file to its computer, trying again while it cannot (until ctx ends or archiveRetryFor
// passes).
func (pl *Plugin) writeArchive(ctx context.Context, a *callArchive) {
	give := time.Now().Add(archiveRetryFor)
	name := a.name()
	for try := 0; ; try++ {
		tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), archiveTryTimeout)
		err := pl.hub.writeFile(tctx, a.machine, a.dir, name, a.markdown())
		cancel()
		var he *hubError
		switch {
		case err == nil:
			pl.logf("call %d: archived %d lines to %s:%s", a.id, len(a.lines), a.machine, path.Join(a.dir, name))
			return
		case errors.As(err, &he) && he.Code == "exists" && try > 0:
			// An earlier try wrote it and its answer was lost (a timeout, a gateway's 502): the name is this run's.
			pl.logf("call %d: archived %d lines to %s:%s (an earlier try wrote it)", a.id, len(a.lines), a.machine, path.Join(a.dir, name))
			return
		case errors.As(err, &he) && he.Status/100 == 4 && he.Status != http.StatusRequestTimeout && he.Status != http.StatusTooManyRequests:
			pl.logf("call %d: archive not written to %s: HTTP %d %s", a.id, a.machine, he.Status, he.Code)
			return
		}
		wait := archiveRetryWaits[min(try, len(archiveRetryWaits)-1)]
		if time.Now().Add(wait).After(give) {
			pl.logf("call %d: archive dropped after %d tries: %s", a.id, try+1, archiveReason(err))
			return
		}
		select {
		case <-ctx.Done():
			pl.logf("call %d: archive dropped: the extension stopped", a.id)
			return
		case <-time.After(wait):
		}
	}
}

// archiveReason is a failed write as the log may say it: the hub's status and code, never a body.
func archiveReason(err error) string {
	var he *hubError
	if errors.As(err, &he) {
		return fmt.Sprintf("HTTP %d %s", he.Status, he.Code)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	return "no answer from the hub"
}

// writeFile creates text file name in dir on machine (POST …/files/mkdir for each folder of dir below the home or
// the root, then …/files/write with create): folders are made when they are not there (those the write guard keeps
// off limits are left to the write); a taken name is the hub's 409 exists, nothing replaced.
func (h *hubClient) writeFile(ctx context.Context, machine, dir, name, text string) error {
	base := "/api/v1/machines/" + url.PathEscape(machine) + "/files/"
	root, rest := "/", strings.TrimPrefix(dir, "/")
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		root, rest = "~", strings.TrimPrefix(strings.TrimPrefix(dir, "~"), "/")
	}
	at := root
	for _, leaf := range strings.Split(rest, "/") {
		if leaf == "" || leaf == "." {
			continue
		}
		err := h.call(ctx, "POST", base+"mkdir", map[string]any{"dir": at, "name": leaf}, nil)
		var he *hubError
		// exists: there already. read_only: a folder above the home (/Users of /Users/me/…), which the write guard
		// does not let anyone make; it is there or the write below says so.
		if err != nil && !(errors.As(err, &he) && (he.Code == "exists" || he.Code == "read_only")) {
			return err
		}
		at = path.Join(at, leaf)
	}
	return h.call(ctx, "POST", base+"write", map[string]any{"path": path.Join(dir, name), "text": text, "create": true}, nil)
}
