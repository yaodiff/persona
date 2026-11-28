import { parseAPIError } from "./api-errors";
import { COMPACTED_TEXT, isCompaction } from "./compaction";
import { activityOf } from "./format";
import { getSession, subscribe } from "./store";
import type { Compaction, Item } from "./types";

function isBoundaryItem(it: Item): boolean {
  return isCompaction(it) || (it.kind === "notice" && !it.task && !it.wake && (it.text ?? "").trim() === COMPACTED_TEXT);
}

export interface TerminalCompactionSnapshot {
  /** Highest `seq` present before `/compact` was sent; older paged/replayed items have `seq <= afterSeq`. */
  afterSeq: number;
  /** Item IDs already present before `/compact` was sent. */
  knownIds: ReadonlySet<string>;
  /** `client_msg_id` of the `/compact` command sent by this recovery flow. */
  commandMsgId: string;
}

/** Captures the transcript watermark right before sending `/compact`. */
export function snapshotTerminalCompaction(items: readonly Item[], commandMsgId: string): TerminalCompactionSnapshot {
  let afterSeq = 0;
  const knownIds = new Set<string>();
  for (const it of items) {
    knownIds.add(it.id);
    if (it.seq > afterSeq) afterSeq = it.seq;
  }
  return { afterSeq, knownIds, commandMsgId };
}

function postCommandItems(items: readonly Item[], snapshot: TerminalCompactionSnapshot): Item[] {
  return items.filter((it) => !snapshot.knownIds.has(it.id) && it.seq > snapshot.afterSeq);
}

/** Whether a new compaction boundary was written strictly after the pre-command watermark (and command item, if present). */
export function hasPostCommandBoundary(items: readonly Item[], snapshot: TerminalCompactionSnapshot): boolean {
  const post = postCommandItems(items, snapshot);
  const cmdUser = post.find((it) => it.client_msg_id === snapshot.commandMsgId && it.role === "user");
  const minSeq = cmdUser ? cmdUser.seq : snapshot.afterSeq;
  return post.some((it) => isBoundaryItem(it) && (cmdUser ? it.seq >= minSeq : it.seq > snapshot.afterSeq));
}

/** Whether any API error or error notice appeared strictly after the pre-command watermark. */
export function hasPostCommandError(items: readonly Item[], snapshot: TerminalCompactionSnapshot): boolean {
  for (const it of postCommandItems(items, snapshot)) {
    if ((it.role === "assistant" && it.kind === "text") || it.kind === "notice") {
      if (it.is_error || parseAPIError(it.text)) return true;
    }
  }
  return false;
}

const DEFAULT_WAIT_MS = 5 * 60_000;
const TERMINAL_SETTLE_MS = 1_500;

function compactionSig(c: Compaction | undefined): string {
  return c ? `${c.started_at}|${c.state}|${c.finished_at ?? ""}` : "";
}

/**
 * Waits for an assistant conversation's `session.compaction` to settle to a new `"done"` or `"failed"` state.
 * Resolves to `"done"`, `"failed"`, or `"cancelled"` (aborted / timed out).
 */
export function waitForAssistantCompaction(
  mid: string,
  sid: string,
  before: Compaction | undefined,
  signal: AbortSignal,
  timeoutMs = DEFAULT_WAIT_MS,
): Promise<"done" | "failed" | "cancelled"> {
  if (signal.aborted) return Promise.resolve("cancelled");
  const prevSig = compactionSig(before);
  const prevStart = before?.started_at ?? "";
  const prevFinish = before?.finished_at ?? "";
  let sawRunning = false;

  const inspect = (): "done" | "failed" | null => {
    const c = getSession(mid, sid)?.compaction;
    if (!c) return null;
    if (c.state === "running") {
      sawRunning = true;
      return null;
    }
    const fresh = sawRunning || compactionSig(c) !== prevSig || c.started_at !== prevStart || (c.finished_at ?? "") !== prevFinish;
    if (!fresh) return null;
    if (c.state === "done") return "done";
    if (c.state === "failed") return "failed";
    return null;
  };

  const immediate = inspect();
  if (immediate) return Promise.resolve(immediate);

  return new Promise((resolve) => {
    let settled = false;
    let timer = 0;
    let unsub: (() => void) | undefined;

    const finish = (outcome: "done" | "failed" | "cancelled") => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      signal.removeEventListener("abort", onAbort);
      unsub?.();
      resolve(outcome);
    };
    const onAbort = () => finish("cancelled");
    const check = () => {
      const result = inspect();
      if (result) finish(result);
    };

    signal.addEventListener("abort", onAbort, { once: true });
    unsub = subscribe(check);
    timer = window.setTimeout(() => finish("cancelled"), timeoutMs);
    check();
  });
}

export interface TerminalCompactionWatcher {
  /** Call once `/compact` has been delivered so idle settlement may finish. */
  markDelivered: () => void;
  /** Cancel and clean up subscriptions immediately (e.g. if `/compact` failed to send). */
  cancel: () => void;
  /** Resolves to `"done"`, `"failed"`, or `"cancelled"`. */
  promise: Promise<"done" | "failed" | "cancelled">;
}

/**
 * Starts watching for a terminal Claude Code `/compact` BEFORE `/compact` is sent, avoiding a race if the terminal
 * transitions `idle -> working -> idle` while the send HTTP request is still in flight.
 *
 * Requires ALL of:
 * 1. The terminal transitioned through `"working"` after watching began (`sawWorking`).
 * 2. The terminal returned to idle (not offline / needs_you).
 * 3. A new compaction boundary appeared strictly after `snapshot.afterSeq` (and not in `snapshot.knownIds`).
 * 4. No post-command API error or error notice appeared (checked unconditionally, even if a boundary also appeared).
 */
export function watchTerminalCompaction(
  mid: string,
  sid: string,
  snapshot: TerminalCompactionSnapshot,
  getItems: () => readonly Item[],
  subscribeItems: (fn: () => void) => () => void,
  signal: AbortSignal,
  timeoutMs = DEFAULT_WAIT_MS,
  settleMs = TERMINAL_SETTLE_MS,
): TerminalCompactionWatcher {
  let settled = false;
  let delivered = false;
  let sawWorking = false;
  let timer = 0;
  let settleTimer = 0;
  let unsubStore: (() => void) | undefined;
  let unsubItems: (() => void) | undefined;
  let resolvePromise!: (outcome: "done" | "failed" | "cancelled") => void;

  const promise = new Promise<"done" | "failed" | "cancelled">((resolve) => {
    resolvePromise = resolve;
  });

  const finish = (outcome: "done" | "failed" | "cancelled") => {
    if (settled) return;
    settled = true;
    clearTimeout(timer);
    clearTimeout(settleTimer);
    signal.removeEventListener("abort", onAbort);
    unsubStore?.();
    unsubItems?.();
    resolvePromise(outcome);
  };
  const onAbort = () => finish("cancelled");

  const check = () => {
    if (settled) return;
    const items = getItems();
    // Unconditional failure check: an error after /compact always fails recovery, even if a boundary also appeared.
    if (hasPostCommandError(items, snapshot)) {
      finish("failed");
      return;
    }
    const sess = getSession(mid, sid);
    if (!sess) return;
    const st = activityOf(sess);
    if (st === "working") {
      sawWorking = true;
      clearTimeout(settleTimer);
      settleTimer = 0;
      return;
    }
    if (st === "offline" || st === "needs_you") {
      finish("failed");
      return;
    }
    // Never resolve done before /compact has been delivered and observed running (`sawWorking`).
    if (!delivered || !sawWorking) return;
    if (hasPostCommandBoundary(items, snapshot)) {
      finish("done");
      return;
    }
    if (!settleTimer) {
      settleTimer = window.setTimeout(() => {
        const latest = getItems();
        if (!hasPostCommandError(latest, snapshot) && hasPostCommandBoundary(latest, snapshot)) finish("done");
        else finish("failed");
      }, settleMs);
    }
  };

  if (signal.aborted) {
    finish("cancelled");
  } else {
    signal.addEventListener("abort", onAbort, { once: true });
    unsubStore = subscribe(check);
    unsubItems = subscribeItems(check);
    timer = window.setTimeout(() => finish("cancelled"), timeoutMs);
    check();
  }

  return {
    markDelivered: () => {
      delivered = true;
      check();
    },
    cancel: () => finish("cancelled"),
    promise,
  };
}

/** Convenience wrapper when `/compact` has already been delivered. */
export function waitForTerminalCompaction(
  mid: string,
  sid: string,
  snapshot: TerminalCompactionSnapshot,
  getItems: () => readonly Item[],
  subscribeItems: (fn: () => void) => () => void,
  signal: AbortSignal,
  timeoutMs = DEFAULT_WAIT_MS,
  settleMs = TERMINAL_SETTLE_MS,
): Promise<"done" | "failed" | "cancelled"> {
  const watcher = watchTerminalCompaction(mid, sid, snapshot, getItems, subscribeItems, signal, timeoutMs, settleMs);
  watcher.markDelivered();
  return watcher.promise;
}
