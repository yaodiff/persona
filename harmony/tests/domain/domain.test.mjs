import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import {
  TranscriptReducer,
  TranscriptSync,
  canAnswerInteraction,
  canSubmitInteractionAnswer,
  createPendingOperation,
  mergeDraftContext,
  mayAutomaticallyRetryOperation,
  mayStartReplacementOperation,
  ownedScopeForItem,
  projectToolTurn,
  receiptGuidance,
  resolveInteractionResult,
  sameScope,
  scopeKey,
  ScopeGeneration,
  ScopedTabRoutes,
  sessionCapabilities,
} from "../../entry/src/main/ets/core/Domain.ts";

const fixtureRoot = new URL("../../../spec/fixtures/", import.meta.url);
const continuityFiles = [
  "continuity/adjacency.json",
  "continuity/append_and_gaps.json",
  "continuity/history_and_cache.json",
  "continuity/pagination_and_legacy.json",
  "continuity/folded_work.json",
];

function readJson(relativePath) {
  return JSON.parse(readFileSync(new URL(relativePath, fixtureRoot), "utf8"));
}

function batchFrom(event) {
  const batch = { items: event.items ?? [] };
  if (event.history !== undefined) batch.history = event.history;
  if (event.from_seq !== undefined) batch.fromSeq = event.from_seq;
  if (event.to_seq !== undefined) batch.toSeq = event.to_seq;
  if (event.backlog !== undefined) batch.backlog = event.backlog;
  if (event.has_more !== undefined) batch.hasMore = event.has_more;
  if (event.more_after !== undefined) batch.moreAfter = event.more_after;
  return batch;
}

function fixtureAction(action) {
  switch (action.kind) {
    case "fetch_after":
      return { kind: action.kind, after_seq: action.afterSeq };
    case "fetch_latest":
      return { kind: action.kind, fold_work: action.foldWork };
    case "subscribe":
      return action.sinceSeq === undefined
        ? { kind: action.kind, fold_work: action.foldWork }
        : { kind: action.kind, fold_work: action.foldWork, since_seq: action.sinceSeq };
    case "fetch_older":
      return { kind: action.kind, before_seq: action.beforeSeq, fold_work: action.foldWork };
    case "fetch_range":
      return { kind: action.kind, from_seq: action.fromSeq, through_seq: action.throughSeq };
  }
  throw new Error(`Unknown action kind: ${action.kind}`);
}

function replayContinuityCase(scenario) {
  const reducer = new TranscriptReducer();
  reducer.restore({
    items: scenario.input.initial.items,
    covered: scenario.input.initial.covered,
    history: scenario.input.initial.history,
    hasMore: scenario.input.initial.has_more,
    loadedOnce: scenario.input.initial.loaded_once,
  });
  const sync = new TranscriptSync(reducer);
  const outputs = [];

  for (const event of scenario.input.events) {
    let gap = "none";
    let actions = [];
    switch (event.kind) {
      case "http_latest": {
        gap = reducer.latest(batchFrom(event));
        actions = sync.settle(gap);
        break;
      }
      case "ws_items": {
        gap = reducer.apply(batchFrom(event));
        actions = sync.settle(gap);
        break;
      }
      case "continue_fill": {
        actions = sync.continueFill();
        break;
      }
      case "http_after": {
        const result = sync.after(batchFrom(event));
        gap = result.gap;
        actions = result.actions;
        break;
      }
      case "http_reload": {
        sync.reload(event.items, event.history ?? null, event.to_seq ?? null, event.has_more);
        break;
      }
      case "load_older": {
        actions = sync.loadOlder();
        break;
      }
      case "http_older": {
        sync.older(event.items, event.has_more);
        break;
      }
      case "reconnect": {
        actions = sync.reconnect();
        break;
      }
      case "open_fold": {
        actions = sync.openFold(event.fold_id);
        break;
      }
      case "http_range": {
        actions = sync.range(event.items, event.more === true);
        break;
      }
      default:
        throw new Error(`Unknown continuity event kind: ${event.kind}`);
    }

    const snapshot = reducer.snapshot();
    outputs.push({
      items: snapshot.items,
      item_ids: snapshot.items.map((item) => item.id),
      covered: snapshot.covered,
      history: snapshot.history,
      has_more: snapshot.hasMore,
      loaded_once: snapshot.loadedOnce,
      gap,
      actions: actions.map(fixtureAction),
    });
  }
  return outputs;
}

for (const fixtureFile of continuityFiles) {
  const fixture = readJson(fixtureFile);
  for (const scenario of fixture.cases) {
    test(`continuity fixture ${scenario.name}`, () => {
      assert.deepStrictEqual(replayContinuityCase(scenario), scenario.expected);
    });
  }
}

const answerTransport = readJson("answer/transport.json");
for (const scenario of answerTransport.cases) {
  test(`answer transport fixture ${scenario.name}`, () => {
    const { operation } = scenario.input;
    const decision = resolveInteractionResult({
      apiAvailable: operation.api_available,
      networkUp: operation.network_up,
      requestedResolution: operation.resolution,
      reply: { outcome: operation.outcome },
    });
    assert.deepStrictEqual({
      allowed: decision.allowed,
      resolution: decision.requestResolution,
      card_present: decision.cardPresent,
    }, scenario.expected);
  });
}

const answerUnified = readJson("answer/unified.json");
for (const scenario of answerUnified.cases.filter((entry) => entry.name.startsWith("state_"))) {
  test(`answer state fixture ${scenario.name}`, () => {
    const state = scenario.input.operation.state;
    assert.equal(canSubmitInteractionAnswer(state), scenario.expected.allowed);
  });
}

test("an already-resolved answer uses the hub's effective result", () => {
  const decision = resolveInteractionResult({
    apiAvailable: true,
    networkUp: true,
    requestedResolution: { allow: true },
    reply: { outcome: "success", already: true, resolution: { allow: false, message: "Denied by the user" } },
  });
  assert.deepStrictEqual(decision.requestResolution, { allow: true });
  assert.deepStrictEqual(decision.effectiveResolution, { allow: false, message: "Denied by the user" });
  assert.equal(decision.alreadyResolved, true);
  assert.equal(decision.cardPresent, false);
});

function lineCase(file, name) {
  const fixture = readJson(`lines/${file}.json`);
  const scenario = fixture.cases.find((entry) => entry.name === name);
  assert.notEqual(scenario, undefined, `Missing line fixture ${name}`);
  return scenario;
}

test("a running turn keeps only its newest assistant text outside the collapsed work row", () => {
  const scenario = lineCase("structure", "live_reply_stays_after_work");
  const projection = projectToolTurn(scenario.input.items[0].id, scenario.input.items, true);
  assert.equal(projection.workKey, "w-1#0");
  assert.equal(projection.summaryVisible, true);
  assert.equal(projection.collapsedByDefault, true);
  assert.equal(projection.expanded, false);
  assert.equal(projection.visibleAssistantText?.id, "2#0");
  assert.equal(projection.visibleTextKind, "interim");
  assert.deepStrictEqual(projection.inspectionSteps, []);
});

test("a completed turn exposes its final text and preserves intermediate narration for inspection", () => {
  const scenario = lineCase("work", "thinking_narration_preserved");
  const turnId = scenario.input.items[0].id;
  const collapsed = projectToolTurn(turnId, scenario.input.items, false);
  assert.equal(collapsed.visibleAssistantText?.id, "3#0");
  assert.equal(collapsed.visibleTextKind, "final");
  assert.equal(collapsed.collapsedByDefault, true);
  assert.equal(collapsed.expanded, false);
  assert.deepStrictEqual(collapsed.inspectionSteps.map((step) => step.id), ["2#0"]);
  assert.equal(collapsed.inspectionSteps[0].item.text, "先读示例配置，再检查依赖。");

  const restoredOpen = projectToolTurn(turnId, scenario.input.items, false, true);
  assert.equal(restoredOpen.workKey, collapsed.workKey);
  assert.equal(restoredOpen.expanded, true);
  assert.deepStrictEqual(restoredOpen.inspectionSteps, collapsed.inspectionSteps);
});

test("tool details attach every result and use the newest result after reload", () => {
  const scenario = lineCase("work", "last_result_for_call_wins");
  const projection = projectToolTurn(scenario.input.items[0].id, scenario.input.items, false, true);
  const tool = projection.inspectionSteps.find((step) => step.kind === "tool");
  assert.notEqual(tool, undefined);
  assert.equal(tool.results.length, 2);
  assert.equal(tool.result?.id, "4#0");
  assert.equal(tool.result?.text, "重试成功");
  assert.equal(tool.failed, false);
  assert.equal(tool.running, false);
  assert.equal(projection.toolCount, 1);
  assert.deepStrictEqual(projection.userImages, []);
});

test("empty thinking is retained between calls and omitted when standalone", () => {
  const standalone = lineCase("work", "standalone_empty_thinking_hidden");
  const standaloneProjection = projectToolTurn(standalone.input.items[0].id, standalone.input.items, false);
  assert.equal(standaloneProjection.thoughtCount, 0);
  assert.deepStrictEqual(standaloneProjection.inspectionSteps.map((step) => step.id), []);

  const between = lineCase("work", "empty_thinking_between_tools_is_retained");
  const betweenProjection = projectToolTurn(between.input.items[0].id, between.input.items, false);
  assert.equal(betweenProjection.thoughtCount, 1);
  assert.deepStrictEqual(betweenProjection.inspectionSteps.map((step) => step.id), ["2#0", "3#0", "4#0"]);
});

test("tool images remain attached to their call in the inspection projection", () => {
  const scenario = lineCase("work", "tool_images_belong_to_call");
  const projection = projectToolTurn(scenario.input.items[0].id, scenario.input.items, false);
  const tool = projection.inspectionSteps.find((step) => step.kind === "tool");
  assert.notEqual(tool, undefined);
  assert.deepStrictEqual({
    userImageIds: projection.userImages.map((item) => item.id),
    toolImageIds: tool.images.map((item) => item.id),
  }, { userImageIds: ["2#0"], toolImageIds: ["5#0"] });
  assert.deepStrictEqual(projection.userMessages.map((item) => item.id), ["1#0"]);
});

test("fold summaries retain their represented tool and thought counts", () => {
  const scenario = lineCase("folds", "fold_counts_tools_and_thoughts");
  const projection = projectToolTurn(scenario.input.items[0].id, scenario.input.items, false, true);
  assert.equal(projection.toolCount, 3);
  assert.equal(projection.thoughtCount, 1);
  assert.equal(projection.inspectionSteps[0].kind, "fold");
  assert.equal(projection.inspectionSteps[0].item.id, "fold:2#0");
});

test("opened folds survive a repeated page; wider folds, replacement, and history reset take effect", () => {
  const prompt = { id: "1#0", seq: 1, role: "user", kind: "text", text: "go" };
  const answer = { id: "20#0", seq: 20, role: "assistant", kind: "text", text: "done" };
  const fold = {
    id: "fold:2#0", seq: 2, kind: "fold",
    fold: { through_seq: 8, through_id: "8#0", tools: 3, thoughts: 1 },
  };
  const work = [{ id: "2#0", seq: 2, kind: "tool_use", tool_use_id: "call-a", tool_name: "Bash" }];
  const page = { items: [prompt, fold, answer], history: "history-a", toSeq: 20, backlog: true, hasMore: false };
  const reducer = new TranscriptReducer();
  reducer.latest(page);
  const sync = new TranscriptSync(reducer);
  assert.deepStrictEqual(sync.openFold(fold.id), [{ kind: "fetch_range", fromSeq: 2, throughSeq: 8 }]);
  assert.deepStrictEqual(sync.range(work, false), []);
  assert.deepStrictEqual(reducer.items.map((item) => item.id), ["1#0", "2#0", "20#0"]);

  reducer.latest(page);
  assert.deepStrictEqual(reducer.items.map((item) => item.id), ["1#0", "2#0", "20#0"]);

  const widerFold = { ...fold, fold: { ...fold.fold, through_seq: 10, through_id: "10#0", tools: 4 } };
  reducer.latest({ ...page, items: [prompt, widerFold, answer] });
  assert.deepStrictEqual(reducer.items.map((item) => item.id), ["1#0", "fold:2#0", "20#0"]);
  assert.equal(reducer.items[1].fold.through_seq, 10);

  reducer.replace(page.items, "history-a", 20, false);
  assert.deepStrictEqual(reducer.items.map((item) => item.id), ["1#0", "fold:2#0", "20#0"]);
  assert.equal(reducer.items[1].fold.through_seq, 8);

  assert.deepStrictEqual(sync.openFold(fold.id), [{ kind: "fetch_range", fromSeq: 2, throughSeq: 8 }]);
  sync.range(work, false);
  reducer.latest({ ...page, history: "history-b" });
  assert.deepStrictEqual(reducer.items.map((item) => item.id), ["1#0", "fold:2#0", "20#0"]);
});

test("real work removes an overlapping fold, including a fold in the same batch", () => {
  const fold = {
    id: "fold:2#0", seq: 2, kind: "fold",
    fold: { through_seq: 8, through_id: "8#0", tools: 3, thoughts: 1 },
  };
  const call = { id: "4#0", seq: 4, kind: "tool_use", tool_use_id: "call-a", tool_name: "Bash" };
  const overlapped = new TranscriptReducer();
  overlapped.latest({ items: [fold], history: "history-a", toSeq: 8, backlog: true });
  overlapped.apply({ items: [call] });
  assert.deepStrictEqual(overlapped.items.map((item) => item.id), ["4#0"]);
  assert.equal(projectToolTurn("turn", overlapped.items, false).toolCount, 1);

  const mixed = new TranscriptReducer();
  mixed.latest({ items: [fold, call], history: "history-a", toSeq: 8, backlog: true });
  assert.deepStrictEqual(mixed.items.map((item) => item.id), ["4#0"]);
  assert.equal(projectToolTurn("turn", mixed.items, false).toolCount, 1);
});

test("assistant narration participates in folds while user text and out-of-range answers remain", () => {
  const user = { id: "2#3", seq: 2, kind: "text", role: "user", text: "Keep this request" };
  const narration = { id: "2#4", seq: 2, kind: "text", role: "assistant", text: "Working through the files" };
  const answer = { id: "2#12", seq: 2, kind: "text", role: "assistant", text: "Final answer" };
  const fold = { id: "fold:2#2", seq: 2, kind: "fold", fold: { through_seq: 2, through_id: "2#10" } };
  const reducer = new TranscriptReducer();
  reducer.latest({ items: [user, narration, answer], history: "narration", toSeq: 2 });
  reducer.apply({ items: [fold] });
  assert.deepStrictEqual(reducer.items.map(item => item.id), [fold.id, user.id, answer.id]);
  reducer.apply({ items: [narration] });
  assert.deepStrictEqual(reducer.items.map(item => item.id), [user.id, narration.id, answer.id]);
  assert.equal(reducer.covered, 2);
});

test("a repeated tool receipt replaces stale error metadata and keeps authoritative supervision visibility", () => {
  const call = { id: "2#0", seq: 2, kind: "tool_use", tool_use_id: "call-a", tool_name: "Bash" };
  const failedReceipt = {
    id: "3#0", seq: 3, role: "user", kind: "tool_result", text: "failed",
    tool_use_id: "call-a", is_error: true, error_code: "old-error", metadata: { attempt: 1 },
    supervision_user: true,
  };
  const successfulReceipt = {
    id: "3#0", seq: 3, role: "user", kind: "tool_result", text: "success",
    tool_use_id: "call-a", metadata: { attempt: 2, status: "complete" },
  };
  const reducer = new TranscriptReducer();
  reducer.latest({ items: [call, failedReceipt], history: "history-a", toSeq: 3, backlog: true, hasMore: false });

  reducer.apply({ items: [successfulReceipt], history: "history-a", fromSeq: 3, toSeq: 3 });

  const items = reducer.items;
  const receipt = items.find((item) => item.id === "3#0");
  const projection = projectToolTurn(call.id, items, false, true);
  const tool = projection.inspectionSteps.find((step) => step.kind === "tool");
  assert.deepStrictEqual(items.map((item) => item.id), ["2#0", "3#0"]);
  assert.equal(items.filter((item) => item.id === "3#0").length, 1);
  assert.deepStrictEqual(receipt, { ...successfulReceipt, supervision_user: true });
  assert.equal(receipt.is_error, undefined);
  assert.equal(receipt.error_code, undefined);
  assert.deepStrictEqual(receipt.metadata, { attempt: 2, status: "complete" });
  assert.equal(projection.toolCount, 1);
  assert.notEqual(tool, undefined);
  assert.equal(tool.results.length, 1);
  assert.equal(tool.result?.text, "success");
  assert.equal(tool.failed, false);
});

test("legacy sessions keep access and sourced sessions require explicit capabilities", () => {
  const legacy = sessionCapabilities({});
  assert.deepStrictEqual({
    read: legacy.canRead,
    send: legacy.canSend,
    interrupt: legacy.canInterrupt,
    approve: legacy.canApprove,
    terminal: legacy.canTerminal,
    attach: legacy.canAttach,
  }, { read: true, send: true, interrupt: true, approve: true, terminal: true, attach: false });

  const partial = sessionCapabilities({ sourceKind: "codex_history", capabilities: { read: true, send: true } });
  assert.equal(partial.canRead, true);
  assert.equal(partial.canSend, true);
  assert.equal(partial.canApprove, false);
  assert.equal(partial.canTerminal, false);
  assert.equal(canAnswerInteraction("pending", { sourceKind: "codex_history", capabilities: { send: true } }, false), false);
  assert.equal(canAnswerInteraction("pending", null, true), true);
  assert.equal(canAnswerInteraction("answered", null, true), false);
});

test("read-only reasons explain capability limits while explicit read-only and identity conflict close writes", () => {
  const caps = { read: true, send: true, interrupt: true, approve: true, terminal: true, attach: true };
  const attachableWithoutLocalHistory = sessionCapabilities({
    sourceKind: "codex_app_server",
    capabilities: { read: true, send: true, attach: true },
    readOnlyReason: "此会话尚未生成本地历史，实时接入后可查看新输出",
  });
  assert.equal(attachableWithoutLocalHistory.readOnly, false);
  assert.equal(attachableWithoutLocalHistory.canSend, true);
  assert.equal(attachableWithoutLocalHistory.canAttach, true);

  const readOnly = sessionCapabilities({ sourceKind: "runtime", capabilities: caps, readOnly: true, readOnlyReason: "View only" });
  assert.equal(readOnly.readOnly, true);
  assert.equal(readOnly.canRead, true);
  assert.equal(readOnly.canSend, false);
  assert.equal(readOnly.canApprove, false);
  assert.equal(readOnly.canTerminal, false);
  assert.equal(readOnly.canAttach, false);

  const conflict = sessionCapabilities({ sourceKind: "identity_conflict", capabilities: caps });
  assert.equal(conflict.readOnly, true);
  assert.equal(conflict.canSend, false);
  assert.equal(conflict.canApprove, false);
  assert.equal(conflict.canTerminal, false);
  assert.equal(conflict.canAttach, false);
});

test("attachment is offered only for a sourced, attach-capable, unattached session", () => {
  assert.equal(sessionCapabilities({ sourceKind: "codex_app_server", capabilities: { attach: true } }).canAttach, true);
  assert.equal(sessionCapabilities({ sourceKind: "codex_app_server", attached: true, capabilities: { attach: true } }).canAttach, false);
  assert.equal(sessionCapabilities({ sourceKind: "codex_app_server", capabilities: { attach: false } }).canAttach, false);
});

test("scope keys keep group and manager ownership separate", () => {
  const defaultManager = { groupId: "hub/a", managerId: "" };
  const workManager = { groupId: "hub/a", managerId: "work" };
  assert.notEqual(scopeKey(defaultManager), scopeKey(workManager));
  assert.notEqual(scopeKey(defaultManager), scopeKey({ groupId: "hub", managerId: "a/" }));
  assert.equal(sameScope(defaultManager, { groupId: "hub/a", managerId: "" }), true);
  assert.deepStrictEqual(ownedScopeForItem("hub/a", "selected", "owner"), { groupId: "hub/a", managerId: "owner" });
  assert.deepStrictEqual(ownedScopeForItem("hub/a", "selected", ""), { groupId: "hub/a", managerId: "" });
});

test("scope generations invalidate work from prior scopes and pairing state", () => {
  const guard = new ScopeGeneration();
  const first = guard.select({ groupId: "hub/a", managerId: "" });
  assert.equal(guard.isCurrent(first), true);
  const same = guard.select({ groupId: "hub/a", managerId: "" });
  assert.equal(same.generation, first.generation);
  assert.equal(guard.isCurrent(first), true);
  guard.select({ groupId: "hub/a", managerId: "work" });
  assert.equal(guard.isCurrent(first), false);
  const current = guard.currentToken();
  assert.notEqual(current, null);
  assert.equal(guard.isCurrent(current), true);
  const invalidated = guard.invalidate();
  assert.notEqual(invalidated, null);
  assert.equal(guard.isCurrent(current), false);
});

test("operation target and payload are snapshotted with receipt-gated retry decisions", () => {
  const target = { path: "/api/v1/sessions/m/s/messages", query: { mode: "chat" } };
  const payload = { text: "Synthetic message", images: [{ mime: "image/png", data: "synthetic" }] };
  const operation = createPendingOperation({
    operationId: "op-first",
    credentialId: "credential-a",
    scope: { groupId: "hub/a", managerId: "assistant" },
    kind: "message",
    target,
    payload,
    createdAt: "2026-11-10T10:00:00Z",
  });
  target.path = "/changed";
  payload.text = "Changed after capture";
  payload.images[0].data = "changed";

  assert.equal(operation.target.path, "/api/v1/sessions/m/s/messages");
  assert.equal(operation.payload.text, "Synthetic message");
  assert.equal(operation.payload.images[0].data, "synthetic");
  assert.equal(Object.isFrozen(operation), true);
  assert.equal(Object.isFrozen(operation.target), true);
  assert.equal(Object.isFrozen(operation.payload.images[0]), true);

  assert.equal(receiptGuidance("pending"), "wait");
  assert.equal(receiptGuidance("done"), "accept_result");
  assert.equal(receiptGuidance("failed"), "show_failure");
  assert.equal(receiptGuidance("unknown"), "query_or_check_by_hand");
  for (const state of ["pending", "done", "failed", "unknown"]) {
    assert.equal(mayAutomaticallyRetryOperation(state), false);
  }
  assert.equal(mayStartReplacementOperation("pending", true), false);
  assert.equal(mayStartReplacementOperation("done", true), false);
  assert.equal(mayStartReplacementOperation("failed", false), false);
  assert.equal(mayStartReplacementOperation("unknown", false), false);
  assert.equal(mayStartReplacementOperation("failed", true), true);
  assert.equal(mayStartReplacementOperation("unknown", true), true);
});

test("merging context leaves the scoped draft and its attachments untouched", () => {
  const draft = {
    scope: { groupId: "hub/a", managerId: "work" },
    targetKind: "session",
    targetId: "session-a",
    text: "Keep this draft exactly",
    attachmentRefs: ["attachment-a"],
    caret: 7,
    updatedAt: "2026-11-10T10:00:00Z",
  };
  const merged = mergeDraftContext(
    draft,
    [{ id: "context-a", title: "Existing context" }],
    [{ id: "context-a", title: "Duplicate context" }, { id: "context-b", title: "New context", kind: "context" }],
  );
  assert.deepStrictEqual(merged.draft, draft);
  assert.deepStrictEqual(merged.contexts, [
    { id: "context-a", title: "Existing context" },
    { id: "context-b", title: "New context", kind: "context" },
  ]);
  assert.equal(draft.text, "Keep this draft exactly");
});

test("tab stacks and selected tabs remain scoped by group and manager", () => {
  const routes = new ScopedTabRoutes();
  const assistant = { groupId: "hub/a", managerId: "assistant" };
  const worker = { groupId: "hub/a", managerId: "worker" };
  routes.selectTab(assistant, "assistant");
  routes.setStack(assistant, "agents", [
    { groupId: "hub/a", kind: "session", machineId: "machine-a", sessionId: "session-a", source: "notification" },
  ]);
  routes.selectTab(worker, "agents");
  routes.setStack(worker, "agents", [
    { groupId: "hub/a", kind: "request", requestId: "request-a", source: "inbox" },
  ]);

  assert.equal(routes.selectedTab(assistant, "agents"), "assistant");
  assert.equal(routes.selectedTab(worker, "assistant"), "agents");
  assert.equal(routes.stack(assistant, "agents")[0].sessionId, "session-a");
  assert.equal(routes.stack(worker, "agents")[0].requestId, "request-a");
  assert.deepStrictEqual(routes.stack(assistant, "inbox"), []);
  routes.clearScope(assistant);
  assert.deepStrictEqual(routes.stack(assistant, "agents"), []);
  assert.equal(routes.selectedTab(assistant, "assistant"), "assistant");
  assert.equal(routes.stack(worker, "agents")[0].requestId, "request-a");
});
