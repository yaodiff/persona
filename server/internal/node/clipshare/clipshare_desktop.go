package clipshare

// The Linux clipboard (clipshare.go), only where the node runs in a graphical session: Wayland through wl-clipboard
// (wl-paste / wl-copy), X11 through xclip. A headless machine (no DISPLAY / WAYLAND_DISPLAY, the node a systemd user
// service on a server) has no clipboard, and says so. KDE's password manager hint (x-kde-passwordManagerHint) marks
// concealed content. No _linux file suffix: tests on any OS exercise it with fakes.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// clipGetenv and clipLookPath are os.Getenv and exec.LookPath (tests give fakes).
var (
	clipGetenv   = os.Getenv
	clipLookPath = exec.LookPath
)

// linuxClip is the tools found for the session: paste reads, copy writes; wayland says which kind they are.
type linuxClip struct {
	reason      string
	wayland     bool
	paste, copy string
}

func newLinuxClip() clipBackend {
	wl, x := clipGetenv("WAYLAND_DISPLAY") != "", clipGetenv("DISPLAY") != ""
	if !wl && !x {
		return unsupportedClip{reason: proto.ClipboardReasonNoDisplay}
	}
	if wl {
		p, perr := clipLookPath("wl-paste")
		c, cerr := clipLookPath("wl-copy")
		if perr == nil && cerr == nil {
			return linuxClip{wayland: true, paste: p, copy: c}
		}
	}
	if x { // XWayland too, when wl-clipboard is missing
		if p, err := clipLookPath("xclip"); err == nil {
			return linuxClip{paste: p, copy: p}
		}
	}
	return unsupportedClip{reason: proto.ClipboardReasonNoTool}
}

func (l linuxClip) support() proto.ClipboardSupport { return proto.ClipboardSupport{Supported: true} }

// types lists the MIME types (targets) on the clipboard; none when nothing is on it (both tools then fail).
func (l linuxClip) types(ctx context.Context) []string {
	var out []byte
	var err error
	if l.wayland {
		out, err = runClipTool(ctx, nil, l.paste, "--list-types")
	} else {
		out, err = runClipTool(ctx, nil, l.paste, "-selection", "clipboard", "-o", "-t", "TARGETS")
	}
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

func (l linuxClip) read(ctx context.Context, mime string) ([]byte, error) {
	if l.wayland {
		return runClipTool(ctx, nil, l.paste, "--no-newline", "--type", mime)
	}
	return runClipTool(ctx, nil, l.paste, "-selection", "clipboard", "-o", "-t", mime)
}

// linuxTextTypes are the targets that carry text, best first.
var linuxTextTypes = []string{"text/plain;charset=utf-8", "UTF8_STRING", "text/plain", "STRING", "TEXT"}

func (l linuxClip) get(ctx context.Context) (proto.ClipboardContent, error) {
	types := l.types(ctx)
	if slices.Contains(types, "x-kde-passwordManagerHint") {
		return proto.ClipboardContent{Kind: proto.ClipboardKindConcealed}, nil
	}
	for _, t := range linuxTextTypes {
		if !slices.Contains(types, t) {
			continue
		}
		b, err := l.read(ctx, t)
		if err != nil {
			return proto.ClipboardContent{}, clipReadErr(err)
		}
		s := string(b)
		return proto.ClipboardContent{Kind: proto.ClipboardKindText, Text: &s}, nil
	}
	if slices.Contains(types, "image/png") {
		b, err := l.read(ctx, "image/png")
		if err != nil {
			return proto.ClipboardContent{}, clipReadErr(err)
		}
		return proto.ClipboardContent{Kind: proto.ClipboardKindImage, Image: &proto.ClipboardImage{Mime: "image/png", Data: b}}, nil
	}
	return proto.ClipboardContent{Kind: proto.ClipboardKindEmpty}, nil
}

func clipReadErr(err error) error {
	if errors.Is(err, errTooMuch) {
		return clipTooLarge("the clipboard", clipOutMost)
	}
	return clipFailed("read the clipboard", err)
}

func (l linuxClip) set(ctx context.Context, mime string, data []byte) error {
	if mime == "" {
		mime = "text/plain;charset=utf-8"
		if !l.wayland {
			mime = "UTF8_STRING"
		}
	}
	var cmd *exec.Cmd
	if l.wayland {
		cmd = exec.CommandContext(ctx, l.copy, "--type", mime)
	} else {
		cmd = exec.CommandContext(ctx, l.copy, "-selection", "clipboard", "-i", "-t", mime)
	}
	// Both stay behind in the background to serve the selection: no pipe of ours may be left with them, or Run would
	// wait for them to exit (they do when something else is copied).
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Run(); err != nil {
		return clipFailed("write the clipboard", err)
	}
	return nil
}
