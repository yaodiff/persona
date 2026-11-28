package extmgr

import (
	"context"
	"errors"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
)

// StoreSecret relays metadata/ciphertext only. In particular a provider's error text is never forwarded:
// it could contain a value from op's stderr. expected pins the provider approved on the card.
func (h *Manager) StoreSecret(ctx context.Context, expected extension.SecretProvider, in wire.SecretStoreParams, approvedBy string) (wire.SecretStoreResult, error) {
	var out wire.SecretStoreResult
	fail := errors.New("secret storage failed; no secret values are included in this error")
	if h.extensions == nil || !h.ready(ctx) || in.Target.Validate() != nil || len(in.Sealed) > wire.SecretStoreSealedMost {
		return out, fail
	}
	ps := h.extensions
	prov, hp, err := ps.secretProvider("op://store/target/field")
	if err != nil || prov != expected || !ps.resolves.allow(prov.Extension) {
		return out, fail
	}
	in.Contribution = prov.Contribution
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err = buswire.Call(cctx, hp.proc, extwire.SecretStoreMethod, in)
	if in.Phase == "commit" {
		// An uncertain reply may still follow a successful write. Every kept launcher value of this provider, for
		// every spelling of its references (title-based ones too), is asked for again at its next use, and still
		// answers while the provider cannot be asked (secret_kept.go).
		ps.keptProviderStored(ctx, prov.Extension)
		ps.staleWords(ctx, prov.Extension)
		action := "secret_stored"
		if err != nil {
			action = "secret_store_failed"
		}
		ps.h.audit(ctx, prov.Extension, action, approvedBy, map[string]any{"target": in.Target, "machine": in.Machine, "session": in.Session})
	}
	if err != nil {
		return wire.SecretStoreResult{}, fail
	}
	return out, nil
}
