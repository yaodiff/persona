//go:build !persona_core_only

package update

import "github.com/nwverse/persona/server/internal/nodeproto"

// ThisEdition is the edition this program was built as: every built-in module.
const ThisEdition = nodeproto.EditionFull
