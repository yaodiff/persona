package proto

import "github.com/nwverse/persona/server/internal/nodeproto"

// session.start accepts a native Codex service tier before the first turn.
func init() { Features = append(Features, nodeproto.SessionStartServiceTier) }
