# Hermes Agent runtime

The Hermes companion connects Nous Research [Hermes Agent](https://hermes-agent.nousresearch.com/docs/user-guide/features/acp)
to the local Persona node using Persona's Runtime SDK and the official `hermes acp` stdio protocol. After the
companion connects, select **Hermes Agent** as the launcher when creating an Agent session in a workspace.
The companion starts only when you run it.

## Run it

Use Node.js 22+ and install Hermes with its ACP support. Configure a working model in the Hermes profile before
starting the companion. For a separate Persona profile, set `HERMES_HOME` to that profile directory when configuring
Hermes and launching the companion.

```sh
hermes acp --check
node integrations/hermes/companion.mjs
```

Pass `--hermes-bin <path>` if the executable is outside the service's PATH. Pass `--socket <path>` for a node
with a nondefault runtime socket. Stop the companion to remove its launcher. Keep one writer per conversation
and use a separate Hermes profile if another ACP client needs to work concurrently with the same set of projects.

## Session behavior

- Start and send text in an existing workspace, follow streamed output and inspect tool calls and results.
- Reopen conversations with saved ACP history in the selected Hermes profile. The companion pages through
  Hermes' ACP catalog and loads the transcript when needed. Empty sessions are live only until their first
  persisted turn. A small mapping in the Hermes profile keeps Persona's session ID stable across restarts.
  The UI may refresh its cursor when the saved transcript differs from the live transcript.
- A discovered session opens from the history returned by `session/load`. Live output, tool approvals,
  working status and cancellation in Persona track prompts sent by this companion. On gateway-backed Hermes,
  an attached ACP client can also receive chunks from another client's prompt, but that prompt's terminal
  response goes to its sender. Use the originating client for a live view of turns started there.
- Retry a send with the same client message ID across a node reconnect. The running companion deduplicates sends.
  On a companion restart, Hermes restores transcript content. The client message IDs and pending turn state are
  local to the previous companion process.
  If a worker disconnects during a prompt, the running companion keeps the locally dispatched input and failure
  notice when reloading the session. Hermes owns the durable history across companion restarts.
- Answer Hermes tool permission requests in Persona. The companion maps approval to **Allow once**. Dismissal
  or loss of the card cancels the permission request. Hermes applies the profile's permission settings.
- Interrupt an active turn. Persona waits for the ACP prompt to settle before confirming. Each working
  conversation has its own ACP process. An idle saved conversation releases its process and can be loaded again.
- Model selection and approval policy come from the Hermes profile. This companion provides text Agent sessions
  and tool permission cards. Image input, mode changes, Assistant dispatch, native questions and plans, and terminal
  controls are outside its supported contract.

## Verify

```sh
npm --prefix integrations/hermes test
PERSONAD=<path-to-personad> HERMES_BIN=<path-to-hermes> node integrations/hermes/e2e/live.mjs
```

The live test creates a temporary node home and Hermes profile and serves a local model fixture. It exercises the
actual Persona launcher and the Hermes ACP process without using a model account. It has been tested with
Hermes Agent 0.21.5 (`v2026.9.24`) and 0.23.3.
