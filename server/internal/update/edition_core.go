//go:build persona_core_only

package update

import "github.com/nwverse/persona/server/internal/nodeproto"

// ThisEdition is the edition this program was built as: Persona Core alone (-tags persona_core_only).
const ThisEdition = nodeproto.EditionCore
