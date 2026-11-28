# Server domain code: the short form

The rules in full, with why each holds, are `docs/server/architecture.md` ("Domain model"); its pitfalls are
`docs/lessons/domain-modeling.md`. This page is what to keep in mind while writing Go under `server/internal`.

**Every package states its layer** on a line of its own in its package comment, `//persona:layer <layer>`, one of
`kit` (only `domainkit`), `core` (one bounded context), `core shared` (values and closed sets every core may use, directly
under `server/internal`), `adapter` (the only code that converts a core to and from contracts, rows and log text),
`application` (clock, ids, IO, goroutines; must show an effect), `application seam`, `library`, `contract` or `testkit`.
A new package declares one; the packages in `server/internal/archguard/testdata/domain_layers/undeclared.txt` predate the rule.

**The rules, by id** (the table in "The rules and their checks" says what each reads):

- Layers: `layer-declared`, `layer-one`, `layer-kit`, `layer-contract`, `layer-shared`, `layer-application`, `layer-library`, `layer-testkit`, `core-importers`, `adapter-no-application`, `adapter-import`, `path-no-core`.
- Inside a core: `core-import`, `core-effect`, `core-clock-call`, `core-package-var`, `core-exported-const`, `core-type-param`, `core-any`, `core-exported-field`, `core-tag`, `core-bare-type`, `core-alias`, `core-field-name`, `core-distinct-shape`, `core-primitive-field`, `core-primitive-signature`, `core-collection`, `core-interface-param`, `core-accessor`, `core-pointer-receiver`, `core-receiver-write`, `core-codec-method`, `core-wire-method`, `core-constructor`, `core-params`, `core-outcome-return`, `core-fail-family`, `core-part-name`, `core-visitor-name`, `core-public-evolve`, `core-service-name`, `core-map-range`, `core-zero-doc`, `core-doc`, `core-test-package`, `core-hook`.
- Seams: `seam-exported-field`, `seam-any`, and `core-accessor` for an exported method on an exported struct whose only statement directly returns a receiver field.
- Outside a core: `out-dot-import`, `out-alias`, `out-literal`, `out-conversion`, `out-wire-ref`, `out-parse-call`, `out-visitor-impl`, `out-clock-read`, `out-slot-mint`, `out-code-mint`, `out-unsafe`, `out-rules-import`, `out-log-use`, `adapter-name`, `adapter-exported-result`.
- Counts that only fall, in code that predates the rules: `app-exported-record`, `app-named-primitive`, `app-lifecycle-write`, `app-bridge-call`.

**What compliant code looks like** (`server/internal/agentref/ref.go`): unexported fields named for their types, the zero
value documented and raised as a defect wherever it is handed in, a predicate (`OnMachine`, `IsSame`) where a caller
would want a getter, and a visitor as the only way out:

```go
// Ref is one session on one machine. It is comparable, so it keys maps. The zero Ref is no session; ParseRef never
// makes one, every method raises a defect on it, and where a session may be absent the type is opt.Option[Ref].
type Ref struct {
	machine MachineID
	session SessionID
}

// Accept tells v the machine and the session.
func (r Ref) Accept(v RefVisitor) {
	r.known("agentref.Ref.Accept")
	v.VisitRef(r.machine, r.session)
}
```

**Start a package with the scaffold**, then edit it into the domain's own words:
`python3 scripts/gen/new_domain_slice.py go internal/<context>/<name> --doc "<one sentence>" [--value …] [--closed …] [--entity … --id …] [--builder] [--decider] [--adapter] [--act]`
(`--help` says what each flag writes). Its every-flag output is `server/internal/archguard/testdata/scaffold`.

**Running the rules:** `go test ./<pkg>/...` runs the domain rules through the package's `rules_test.go`, and names each
broken rule with its fix; check-fast runs archguard (`TestDomainLayers`) for every server change.

**Copy these:** `server/internal/domainkit`, `server/internal/agentref` and its `agentrefadapt`, and the compliant twins
under `server/internal/archguard/testdata/domainrules/planted` (`assistant/fleetcount`, `assistant/inbox/item`). Packages
with lines in `server/internal/archguard/testdata/domain_layers/baseline.txt` predate the rules: do not copy them.
