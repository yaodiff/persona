//go:build !windows

package clipshare

import (
	"context"

	"github.com/nwverse/persona/server/internal/proto"
)

// winClip is the Windows clipboard (clipshare_windows.go); elsewhere installOS never asks for it.
type winClip struct{}

func (winClip) support() proto.ClipboardSupport {
	return proto.ClipboardSupport{Reason: proto.ClipboardReasonOS}
}
func (winClip) get(context.Context) (proto.ClipboardContent, error) {
	return proto.ClipboardContent{}, clipUnsupported(proto.ClipboardReasonOS)
}
func (winClip) set(context.Context, string, []byte) error {
	return clipUnsupported(proto.ClipboardReasonOS)
}
