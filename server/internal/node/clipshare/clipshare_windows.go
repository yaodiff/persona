package clipshare

// The Windows clipboard through user32 (clipshare.go): CF_UNICODETEXT for text; for images the registered "PNG"
// format when an app put one there (browsers, Office), else CF_DIB turned into PNG (clipdib.go). Setting an image puts
// both PNG and CF_DIB. Content marked with ExcludeClipboardContentFromMonitorProcessing (what password managers set
// to keep clipboard monitors away) is reported as concealed. The node runs in the user's interactive session (a
// scheduled task at login, docs/operations/windows.md); a node in session 0 has no desktop and so no clipboard.

import (
	"context"
	"errors"
	"runtime"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/nwverse/persona/server/internal/proto"
)

const (
	cfDIB         = 8
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

var (
	user32                         = windows.NewLazySystemDLL("user32.dll")
	kernel32                       = windows.NewLazySystemDLL("kernel32.dll")
	procOpenClipboard              = user32.NewProc("OpenClipboard")
	procCloseClipboard             = user32.NewProc("CloseClipboard")
	procEmptyClipboard             = user32.NewProc("EmptyClipboard")
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procSetClipboardData           = user32.NewProc("SetClipboardData")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procRegisterClipboardFormatW   = user32.NewProc("RegisterClipboardFormatW")
	procGlobalAlloc                = kernel32.NewProc("GlobalAlloc")
	procGlobalFree                 = kernel32.NewProc("GlobalFree")
	procGlobalLock                 = kernel32.NewProc("GlobalLock")
	procGlobalUnlock               = kernel32.NewProc("GlobalUnlock")
	procGlobalSize                 = kernel32.NewProc("GlobalSize")
	procRtlMoveMemory              = kernel32.NewProc("RtlMoveMemory")
)

// copyFrom copies n bytes at the locked global memory p (moved by RtlMoveMemory: Go never holds p as a pointer).
func copyFrom(p uintptr, n int) []byte {
	b := make([]byte, n)
	if n > 0 {
		procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&b[0])), p, uintptr(n))
	}
	return b
}

// copyTo copies b to the locked global memory p.
func copyTo(p uintptr, b []byte) {
	if len(b) > 0 {
		procRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	}
}

type winClip struct{}

func (winClip) support() proto.ClipboardSupport {
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err == nil && session == 0 {
		return proto.ClipboardSupport{Reason: proto.ClipboardReasonNoSession}
	}
	return proto.ClipboardSupport{Supported: true}
}

func registerFormat(name string) uintptr {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0
	}
	f, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(p)))
	return f
}

func formatAvailable(f uintptr) bool {
	if f == 0 {
		return false
	}
	ok, _, _ := procIsClipboardFormatAvailable.Call(f)
	return ok != 0
}

// withClipboard opens the clipboard on a locked thread (it belongs to the thread that opened it), trying again
// while another app holds it, and runs fn.
func withClipboard(ctx context.Context, fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			break
		}
		select {
		case <-ctx.Done():
			return clipFailed("open the clipboard", errors.New("another app holds it"))
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer procCloseClipboard.Call()
	return fn()
}

// clipData copies the clipboard's data in format f (nil when it holds none); more than most bytes is errTooMuch,
// never a part of it passed off as the whole.
func clipData(f uintptr, most int) ([]byte, error) {
	h, _, _ := procGetClipboardData.Call(f)
	if h == 0 {
		return nil, nil
	}
	size, _, _ := procGlobalSize.Call(h)
	if size > uintptr(most) {
		return nil, errTooMuch
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return nil, nil
	}
	defer procGlobalUnlock.Call(h)
	return copyFrom(p, int(size)), nil
}

// clipTextMostUTF16 is the most UTF-16 text read: more than this many code units is more than ClipboardTextMost
// bytes of UTF-8 however it is written.
const clipTextMostUTF16 = 2*proto.ClipboardTextMost + 2

// clipDIBMost bounds an uncompressed bitmap read (a 4K screen at 32 bits is about 33 MB).
const clipDIBMost = 64 << 20

func (winClip) get(ctx context.Context) (proto.ClipboardContent, error) {
	var out proto.ClipboardContent
	err := withClipboard(ctx, func() error {
		if formatAvailable(registerFormat("ExcludeClipboardContentFromMonitorProcessing")) {
			out.Kind = proto.ClipboardKindConcealed
			return nil
		}
		if formatAvailable(cfUnicodeText) {
			b, err := clipData(cfUnicodeText, clipTextMostUTF16)
			if err != nil {
				return clipTooLarge("the text", clipTextMostUTF16)
			}
			u := make([]uint16, len(b)/2)
			for i := range u {
				u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
			}
			for i, c := range u {
				if c == 0 {
					u = u[:i]
					break
				}
			}
			t := string(utf16.Decode(u))
			out = proto.ClipboardContent{Kind: proto.ClipboardKindText, Text: &t}
			return nil
		}
		if f := registerFormat("PNG"); formatAvailable(f) {
			b, err := clipData(f, proto.ClipboardImageMost)
			if err != nil {
				return clipTooLarge("the image", proto.ClipboardImageMost)
			}
			if len(b) > 0 {
				out = proto.ClipboardContent{Kind: proto.ClipboardKindImage, Image: &proto.ClipboardImage{Mime: "image/png", Data: trimPNG(b)}}
				return nil
			}
		}
		if formatAvailable(cfDIB) {
			b, err := clipData(cfDIB, clipDIBMost)
			if err != nil {
				return clipTooLarge("the image", clipDIBMost)
			}
			png, err := dibToPNG(b)
			if err != nil {
				return clipFailed("read the image", err)
			}
			out = proto.ClipboardContent{Kind: proto.ClipboardKindImage, Image: &proto.ClipboardImage{Mime: "image/png", Data: png}}
			return nil
		}
		out.Kind = proto.ClipboardKindEmpty
		return nil
	})
	return out, err
}

// putData hands a copy of b to the clipboard in format f; the clipboard owns the memory once that succeeds.
func putData(f uintptr, b []byte) error {
	h, _, err := procGlobalAlloc.Call(gmemMoveable, uintptr(len(b)))
	if h == 0 {
		return err
	}
	p, _, err := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return err
	}
	copyTo(p, b)
	procGlobalUnlock.Call(h)
	if r, _, err := procSetClipboardData.Call(f, h); r == 0 {
		procGlobalFree.Call(h)
		return err
	}
	return nil
}

func (winClip) set(ctx context.Context, mime string, data []byte) error {
	var dib, png []byte
	if mime != "" { // decoded before the clipboard is opened: it is held as short as possible
		var err error
		if dib, png, err = imageToDIB(data); err != nil {
			return clipFailed("read the image", err)
		}
	}
	return withClipboard(ctx, func() error {
		if r, _, err := procEmptyClipboard.Call(); r == 0 {
			return clipFailed("empty the clipboard", err)
		}
		if mime == "" {
			u := append(utf16.Encode([]rune(string(data))), 0)
			b := make([]byte, len(u)*2)
			for i, c := range u {
				b[2*i], b[2*i+1] = byte(c), byte(c>>8)
			}
			if err := putData(cfUnicodeText, b); err != nil {
				return clipFailed("write the clipboard", err)
			}
			return nil
		}
		if f := registerFormat("PNG"); f != 0 {
			_ = putData(f, png) // optional: CF_DIB below is what every app reads
		}
		if err := putData(cfDIB, dib); err != nil {
			return clipFailed("write the clipboard", err)
		}
		return nil
	})
}
