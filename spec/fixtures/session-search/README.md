# Runtime session search

`cases.json` is shared by the server's runtime search and the web palette's existing
`agentSearchHit` matcher. It contains synthetic complete session rows, machine
labels, pinned session identities, pending interactions and a fixed clock. Each case
names its query and ordered expected `(machine_id, session_id, score)` results.
The score includes the palette's five-point bonus for a non-offline session.

The cases cover explicit operator aliases, OR within one dimension and AND across
dimensions, literal unknown operators, machine names, canonical runtimes, model
labels, pin and pending-interaction context, time windows, metadata evidence and
Markdown last-answer text. Two computers deliberately share a session ID. These
fixtures exercise metadata and last-answer search, not complete transcript search
or external runtime execution.

Run the server corpus with `go test ./internal/sessionsearch` from `server/`, and
the client corpus with `npx vitest run src/lib/agent-search.parity.test.ts` from
`web/`. HTTP ownership, private rows, summary truncation, cursor snapshots and
page bounds have separate server regression tests.

The `query` alias equals `input.query` for the existing replay readers. Unknown
operators stay literal text. The existing `since:48h` and `since:2d` aliases both
mean the 24-hour window; this change does not silently redefine that client rule.
