package proto

import (
	"fmt"
	"time"
)

// Subscription rate-limit windows (#2432, docs/server/usage-windows.md): how much of an account's 5-hour and weekly allowance
// is used. A node reads them from Codex app-server or from local files and answers usage.windows. The usage manager
// groups the machines' answers. No credential or raw upstream account ID crosses this boundary.

// UsageReadingSource describes how a node obtained a usage reading, when known.
type UsageReadingSource string

const UsageReadingRollout UsageReadingSource = "rollout"

// UsageWindow is one rate-limit window of one account.
type UsageWindow struct {
	// Label is the window's length as people say it: "5h", "7d" (see WindowLabel).
	Label         string  `json:"label"`
	WindowMinutes int     `json:"window_minutes"`
	UsedPercent   float64 `json:"used_percent"`
	// ResetsAt is when the window starts over; nil when the source did not say.
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	// Model names the one model this window measures, for an allowance a provider keeps per model next to the plan's
	// own (#2595); empty for the plan's window. Such a window never identifies an account.
	Model string `json:"model,omitempty"`
	// Group, Title and Note are set by a usage extension (docs/extensions/extensions.md "Usage"; docs/server/usage-windows.md
	// "Usage extensions"): windows next to each other with the same Group are drawn as one group, Title is the row's own
	// name and Note a short remark under it, all in the language the hub asked in. Plain text, no colour or icon.
	// Without a Group a window is drawn as it always was; a client that does not know these fields ignores them.
	Group string `json:"group,omitempty"`
	Title string `json:"title,omitempty"`
	Note  string `json:"note,omitempty"`
	// ProtectPercent is the used percentage at which the source stops sending work to this account for this window (an
	// account-pool gateway's quota protection line; it can differ per account and per window). Set by a usage extension
	// only, 0 to 100; nil when the source did not say. A client draws a marker on the window's bar at that percentage
	// and ignores the field when it is absent or unknown to it.
	ProtectPercent *float64 `json:"protect_percent,omitempty"`
}

// UsageBalance is a prepaid account's remaining amount as its provider reports it (#11825), for a provider that has
// no rate-limit window to draw (the official DeepSeek launcher's GET /user/balance). Amount is the provider's own
// decimal string, kept exactly as sent ("12.34") and never recomputed into a percentage; Currency is its code ("CNY").
type UsageBalance struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// UsageAccount is one subscription account as one source saw it (a node's answer) or as the hub merged it.
type UsageAccount struct {
	// ID is the manager's opaque row key. For Codex it identifies an account or an unidentified source across resets.
	ID       string `json:"id,omitempty"`
	Provider string `json:"provider"`       // codex | claude
	Name     string `json:"name"`           // "Codex Pro", "Claude"
	Plan     string `json:"plan,omitempty"` // as the source calls it ("pro")
	// Headline and Notice are set by a usage extension: a line of its own under the account's name (what the pool is
	// doing) and a line of explanation when it has no numbers to draw. Plain text in the language the hub asked in.
	Headline string `json:"headline,omitempty"`
	Notice   string `json:"notice,omitempty"`
	// Windows are ordered shortest first. Empty for an account whose provider reports only a balance.
	Windows []UsageWindow `json:"windows"`
	// Balance is the remaining prepaid amount, when the provider reports one instead of rate-limit windows. The
	// account's SampledAt, Stale, StaleAt and Problem apply to it as they do to windows. A genuine zero is a Balance
	// with amount "0.00"; a failed read is Problem with the previous Balance kept, or no Balance at all.
	Balance *UsageBalance `json:"balance,omitempty"`
	// ReadingSource is "rollout" when the node read a Codex rollout file. Omitted for other readings and by older nodes.
	ReadingSource UsageReadingSource `json:"reading_source,omitempty"`
	// SampledAt is when the source wrote the newest of these numbers (not when we read them).
	SampledAt time.Time `json:"sampled_at"`
	// Stale: the sample is older than the collector's schedule allows (its source has not been written to for a
	// while, or the machine that had it is offline).
	Stale bool `json:"stale,omitempty"`
	// StaleAt is when the sample turns stale if nothing newer comes: the sample time plus its source's threshold (#1905).
	// A client that keeps this answer while refreshes fail judges stale itself, Stale || now >= StaleAt, so old numbers
	// fade however long ago it last heard from the hub, and no client hard-codes the thresholds.
	StaleAt *time.Time `json:"stale_at,omitempty"`

	// Where the numbers came from (#2595), set by the hub. Local: read on one of the machines
	// (docs/server/usage-windows.md); Sources: the names of the account-pool gateways that reported it
	// (docs/server/usage-windows.md "Account pool gateways"). An account both saw lists both.
	Local   bool     `json:"local,omitempty"`
	Sources []string `json:"sources,omitempty"`
	// Machines are the names of the computers whose agents' files reported it (#1907), set with Local; clients name
	// those instead of a word like "this computer", which reads as the device in hand.
	Machines []string `json:"machines,omitempty"`
	// Problem: "error" when the gateway could not read the account's numbers just now (it kept the stored ones). When
	// the gateway's own last read failed and this is its last answer, why: "rejected" (it turned the key away, 401 /
	// 403), "unreachable" (it could not be asked: no connection, a timeout) or "failed" (it answered, but not with
	// usage: 404, 5xx, a bad answer). Never carries the text; clients show nothing for a value they don't know.
	Problem string `json:"problem,omitempty"`
	// Ref is the account's identity at its gateway ("<source id>:<account id>"), when the source has one. Hub-internal:
	// it is never sent out. Two samples with refs from one source but different accounts are never merged.
	Ref string `json:"-"`
	// BalanceCredential is a non-secret digest of the resolved credential a prepaid balance was read with (#11825),
	// empty when the key could not be opened or resolved. The usage manager keeps it to tell a replaced or rotated key
	// from the one the last amount was read with, so another account's amount is never shown as this credential's last
	// known balance. Hub-internal: never sent out.
	BalanceCredential string `json:"-"`
	// BalanceSealedCredential is a non-secret digest of the launcher's sealed credential material (#11825). Tracked
	// separately from BalanceCredential so a temporary open/resolution failure of unchanged sealed material (such as a
	// locked vault behind an op:// reference) keeps the last amount marked stale, while replacing the sealed material
	// clears it even if the replacement cannot be opened. Hub-internal: never sent out.
	BalanceSealedCredential string `json:"-"`
	// BalanceSkipped is true when the poll's overall deadline ended before this launcher got a full attempt (#11825).
	// The usage manager keeps whatever state it already had instead of marking the launcher unreachable or removing it.
	// Hub-internal: never sent out.
	BalanceSkipped bool `json:"-"`
	// CodexAccountKey is the node's digest of the upstream account ID, used only by the usage manager.
	CodexAccountKey string `json:"-"`
	// Pass: a usage extension's row (usagesource.FromExtension), drawn exactly as the extension sent it and never merged
	// with the accounts the computers and the other gateways report. Hub-internal.
	Pass bool `json:"-"`
	// Origin distinguishes unidentified sources. On a machine it uses the machine ID and source digest or legacy
	// answer position. On a gateway it uses the reference or a gateway-scoped position. It stays in the usage manager.
	Origin string `json:"-"`
}

// UsageNodeAccount carries internal grouping keys on the node RPC only. Embedding preserves the flat account shape
// older managers and nodes use. The public usage endpoint returns UsageAccount without these keys.
type UsageNodeAccount struct {
	UsageAccount
	AccountKey string `json:"account_key,omitempty"` // digest of the Codex ID from this usage snapshot
	SourceKey  string `json:"source_key,omitempty"`  // digest of the Codex home, scoped to the reporting machine
}

// UsageSnapshot is what a node answers to usage.windows.
type UsageSnapshot struct {
	// Enabled false: hub.json machines[].usage_windows is false for this machine; nothing is read.
	Enabled     bool               `json:"enabled"`
	CollectedAt time.Time          `json:"collected_at"`
	Accounts    []UsageNodeAccount `json:"accounts"`
}

// WindowLabel names a window by its length: 300 → "5h", 10080 → "7d", 45 → "45m"; 0 or less → "".
func WindowLabel(minutes int) string {
	switch {
	case minutes <= 0:
		return ""
	case minutes%(24*60) == 0:
		return fmt.Sprintf("%dd", minutes/(24*60))
	case minutes%60 == 0:
		return fmt.Sprintf("%dh", minutes/60)
	}
	return fmt.Sprintf("%dm", minutes)
}
