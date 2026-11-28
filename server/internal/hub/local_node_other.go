//go:build !linux && !darwin

package hub

import (
	"context"

	"github.com/nwverse/persona/server/internal/update"
)

// Other platforms' managed nodes converge over their resident update RPC.
func syncLocalNode(context.Context, string, serviceFinder, *update.Manifest) error { return nil }
