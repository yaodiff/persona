package extwire

import (
	"encoding/json"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/buswire"
)

// These methods run in the extension process.
const (
	MethodAuthRelayComplete = "auth_relay.complete"
	MethodSecretsDescribe   = "secrets.describe"
	MethodSecretsResolve    = "secrets.resolve"
	MethodSecretsStore      = "secrets.store"
	MethodUIImage           = "ui.image"
	MethodSettingsCheck     = "settings.check"
	MethodShortcutRun       = "shortcut.run"
	MethodOffloadCancel     = "offload.cancel"
	MethodMemoryEntries     = "memory.entries"
)

var (
	AuthRelayCompleteMethod = buswire.Method[wire.AuthRelayCompleteParams, wire.AuthRelayResult]{Name: MethodAuthRelayComplete}
	SecretsDescribeMethod   = buswire.Method[wire.SecretsDescribeParams, wire.SecretsDescribeResult]{Name: MethodSecretsDescribe}
	SecretsResolveMethod    = buswire.Method[wire.SecretsResolveParams, wire.SecretsResolveResult]{Name: MethodSecretsResolve}
	SecretStoreMethod       = buswire.Method[wire.SecretStoreParams, wire.SecretStoreResult]{Name: MethodSecretsStore}
	UIImageMethod           = buswire.Method[wire.UIImageParams, wire.UIImage]{Name: MethodUIImage}
	SettingsCheckMethod     = buswire.Method[wire.SettingsCheckParams, wire.SettingsCheckResult]{Name: MethodSettingsCheck}
	ShortcutRunMethod       = buswire.Method[wire.ShortcutRunParams, wire.ShortcutResult]{Name: MethodShortcutRun}
	OffloadCancelMethod     = buswire.Method[wire.OffloadJobParams, buswire.None]{Name: MethodOffloadCancel}
	// The caller checks the raw page size and shape before decoding entries.
	MemoryEntriesMethod = buswire.Method[wire.MemoryEntriesParams, json.RawMessage]{Name: MethodMemoryEntries}
)
