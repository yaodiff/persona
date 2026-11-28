package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Secret providers (docs/extensions/secret-providers.md): the value of a reference (op://vault/item/field) comes from
// the extension whose secret_providers contribution claims its scheme, asked here and nowhere else. A value is never
// logged, put in an error, an audit detail or an event; only the caller that asked gets it.

var (
	// secretCallTimeout bounds one secrets.resolve or secrets.describe.
	secretCallTimeout = 10 * time.Second
	// secretResolvesMost resolutions per provider per secretResolveWindow; more are refused before reaching it.
	secretResolvesMost  = 60
	secretResolveWindow = time.Minute
)

// SecretError is why a reference did not resolve. Code is for callers (no_provider, provider_off, not_found,
// not_signed_in, rate_limited, refused, failed); the words read in the asker's language.
type SecretError struct {
	Code string
	// RetryAfter is the provider's own wait for rate_limited, in seconds (0: unknown).
	RetryAfter float64
	err        error
}

// Error is the error's words; one made with only a Code (a test's) is its code.
func (e *SecretError) Error() string {
	if e.err == nil {
		return e.Code
	}
	return e.err.Error()
}

func (e *SecretError) Unwrap() error { return e.err }

// Text is the error in l.
func (e *SecretError) Text(l i18n.Lang) string {
	if e.err == nil {
		return e.Code
	}
	return i18n.Localize(e.err, l)
}

// NewSecretError is the SecretError of code with its words (hub-extension_secrets.<code>), for a fake provider.
func NewSecretError(code string, args i18n.Args) *SecretError { return secretErr(code, args) }

func secretErr(code string, args i18n.Args) *SecretError {
	return &SecretError{Code: code, err: i18n.Err("hub-extension_secrets."+code, args)}
}

// SecretDescription is a reference in words for people: whose it is and what it names, never a value.
type SecretDescription struct {
	Provider, ProviderName string
	Title, Detail          string
}

// secretWords keeps references' descriptions (secret_words.go): settings pages and cards show them often, and asking
// the provider each time costs its service a request, or the user an authorization.
type secretWords struct {
	mu   sync.Mutex
	kept map[string]keptWords
	// gen counts the edits: words described while one happened are not kept (their provider may have just gone).
	gen uint64
	// loaded: the sealed words were read from the database; nothing is written before.
	loaded bool
	saveMu sync.Mutex
}

type keptWords struct {
	d  SecretDescription
	at time.Time
	// stale: the provider stored a secret since: asked again at the next describe, answering while it cannot be.
	stale bool
}

// ResolveSecret is reference's value for purpose, from the provider that claims its scheme. approvedBy is who approved
// this use: "settings" (a reference saved in Settings), "rule" (an Always rule) or "device:<id>" (the device that
// approved its hand-off card); it goes into the record of the use with the reference and the purpose. Errors are
// *SecretError.
func (h *Manager) ResolveSecret(ctx context.Context, reference string, purpose wire.SecretPurpose, approvedBy string) (string, error) {
	return h.resolveSecret(ctx, reference, purpose, approvedBy, false)
}

// resolveSecret is ResolveSecret; quiet: the caller still holds an earlier value and uses it if this fails, so the
// provider must not ask the user (SecretsResolveParams.Quiet).
func (h *Manager) resolveSecret(ctx context.Context, reference string, purpose wire.SecretPurpose, approvedBy string, quiet bool) (string, error) {
	if h.extensions == nil || !h.ready(ctx) {
		return "", secretErr("provider_off", i18n.Args{"provider": schemeOf(reference)})
	}
	return h.extensions.resolveSecret(ctx, reference, purpose, approvedBy, quiet)
}

// DescribeSecret is reference in words (cards, settings), from its provider. Errors are *SecretError.
func (h *Manager) DescribeSecret(ctx context.Context, reference string) (SecretDescription, error) {
	if h.extensions == nil || !h.ready(ctx) {
		return SecretDescription{}, secretErr("provider_off", i18n.Args{"provider": schemeOf(reference)})
	}
	return h.extensions.describeSecret(ctx, reference)
}

// SecretProviderOf is the enabled provider of reference's scheme; false for no such provider or text that is not a
// reference.
func (h *Manager) SecretProviderOf(reference string) (extension.SecretProvider, bool) {
	if h.extensions == nil {
		return extension.SecretProvider{}, false
	}
	scheme, ok := extension.ReferenceScheme(reference)
	if !ok {
		return extension.SecretProvider{}, false
	}
	return h.extensions.reg.SecretProviderFor(scheme)
}

func schemeOf(reference string) string {
	if s, ok := extension.ReferenceScheme(reference); ok {
		return s + "://"
	}
	return "?"
}

// secretProvider is the provider of reference and its running program.
func (ps *extensions) secretProvider(reference string) (extension.SecretProvider, *hubExtension, error) {
	scheme, ok := extension.ReferenceScheme(reference)
	if !ok {
		return extension.SecretProvider{}, nil, secretErr("not_reference", nil)
	}
	prov, ok := ps.reg.SecretProviderFor(scheme)
	if !ok {
		return extension.SecretProvider{}, nil, secretErr("no_provider", i18n.Args{"scheme": scheme + "://"})
	}
	hp := ps.get(prov.Extension)
	if hp == nil || hp.proc == nil {
		return prov, nil, secretErr("provider_off", i18n.Args{"provider": prov.Name})
	}
	return prov, hp, nil
}

func (ps *extensions) resolveSecret(ctx context.Context, reference string, purpose wire.SecretPurpose, approvedBy string, quiet bool) (string, error) {
	prov, hp, err := ps.secretProvider(reference)
	if prov.Extension == "" {
		ps.forgetReference(ctx, reference) // no provider claims it (secret_kept.go)
		return "", err                     // no provider to record it on
	}
	detail := map[string]any{"reference": reference, "purpose": purpose}
	fail := func(e *SecretError) (string, error) {
		if revokes(e.Code) {
			ps.forgetReference(ctx, reference) // gone or refused: a kept launcher key of it is not trusted any more (secret_kept.go)
		} // one that may pass (not signed in, off, rate limited, a timeout) leaves what is kept
		detail["code"] = e.Code
		ps.h.audit(ctx, prov.Extension, "secret_resolve_failed", approvedBy, detail)
		ps.logf("plugin %s: secrets.resolve for %s %s: %s", prov.Extension, purpose.Kind, purpose.ID, e.Code)
		return "", e
	}
	if err != nil {
		return fail(err.(*SecretError))
	}
	if !ps.resolves.allow(prov.Extension) {
		return fail(secretErr("rate_limited", i18n.Args{"provider": prov.Name}))
	}
	cctx, cancel := context.WithTimeout(ctx, secretCallTimeout)
	defer cancel()
	res, err := buswire.Call(cctx, hp.proc, extwire.SecretsResolveMethod,
		wire.SecretsResolveParams{Contribution: prov.Contribution, Reference: reference, Purpose: purpose, Quiet: quiet})
	switch {
	case err != nil:
		return fail(providerError(prov, err, cctx.Err() != nil && ctx.Err() == nil))
	case res.Value == "":
		return fail(secretErr("failed", i18n.Args{"provider": prov.Name, "why": "no value"}))
	case len(res.Value) > extension.SecretMost:
		return fail(secretErr("too_long", i18n.Args{"provider": prov.Name}))
	}
	ps.h.audit(ctx, prov.Extension, "secret_resolved", approvedBy, detail)
	ps.logf("plugin %s: secrets.resolve for %s %s", prov.Extension, purpose.Kind, purpose.ID)
	// The record for its own page; a provider that does not keep one ignores it.
	_ = hp.proc.Notify("secrets.used", wire.SecretsUsedEvent{Contribution: prov.Contribution, Reference: reference,
		Purpose: purpose, At: time.Now().UTC(), ApprovedBy: approvedBy})
	return res.Value, nil
}

func (ps *extensions) describeSecret(ctx context.Context, reference string) (SecretDescription, error) {
	prov, hp, err := ps.secretProvider(reference)
	if err != nil {
		return SecretDescription{}, err
	}
	ps.loadWords(ctx)
	ps.words.mu.Lock()
	k, ok := ps.words.kept[reference]
	gen := ps.words.gen
	ps.words.mu.Unlock()
	ok = ok && k.d.Provider == prov.Extension
	if ok && !k.stale {
		return k.d, nil // kept at any age: the words of a reference do not lapse (secret_words.go)
	}
	cctx, cancel := context.WithTimeout(ctx, secretCallTimeout)
	defer cancel()
	res, err := buswire.Call(cctx, hp.proc, extwire.SecretsDescribeMethod,
		wire.SecretsDescribeParams{Contribution: prov.Contribution, Reference: reference})
	if err != nil {
		if ok {
			return k.d, nil // stale, and the provider cannot say otherwise now
		}
		return SecretDescription{}, providerError(prov, err, cctx.Err() != nil && ctx.Err() == nil)
	}
	d := SecretDescription{Provider: prov.Extension, ProviderName: prov.Name, Title: providerWords(res.Title, 200),
		Detail: providerWords(res.Detail, secretDetailMost)}
	if d.Title == "" {
		d.Title = reference
	}
	ps.keepWords(ctx, reference, d, gen)
	return d, nil
}

// providerError is a provider's failure as a SecretError: its code when it is one the protocol names, its words (one
// line, clipped) otherwise. timedOut: the call ran out of secretCallTimeout.
func providerError(prov extension.SecretProvider, err error, timedOut bool) *SecretError {
	args := i18n.Args{"provider": prov.Name}
	if timedOut {
		args["why"] = "no answer in " + secretCallTimeout.String()
		return secretErr("failed", args)
	}
	var re *rpc.Error
	if !errors.As(err, &re) {
		args["why"] = providerWords(err.Error(), 200)
		return secretErr("failed", args)
	}
	switch re.Code {
	case "not_found", "not_signed_in":
		return secretErr(re.Code, args)
	case "rate_limited":
		e := secretErr("rate_limited", args)
		var d struct {
			RetryAfter float64 `json:"retry_after"`
		}
		if json.Unmarshal(re.Data, &d) == nil && d.RetryAfter > 0 {
			e.RetryAfter = d.RetryAfter
		}
		return e
	case "refused":
		args["why"] = providerWords(re.Message, 200)
		return secretErr("refused", args)
	case extension.CodeUnknownMethod, extension.CodeNoContribution:
		args["why"] = "it does not resolve references (an older version?)"
		return secretErr("failed", args)
	}
	args["why"] = providerWords(re.Message, 200)
	return secretErr("failed", args)
}

// secretDetailMost is the most characters of a provider's detail (secrets.describe) the host keeps, on one line, a
// longer one cut with "…": what fits after the item's name in a card's line on a phone (docs/extensions/secret-providers.md
// "Methods"). Every page and card reads the cut one; a provider's own restraint is not relied on.
const secretDetailMost = 40

// providerWords is s on one line, its words joined by single spaces, cut to most characters with "…".
func providerWords(s string, most int) string {
	return clip(strings.Join(strings.Fields(s), " "), most)
}
