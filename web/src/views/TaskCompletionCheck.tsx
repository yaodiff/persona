import { useCallback, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { currentLanguage, t } from "../i18n";
import { credentialOwner, onCredentialChange, subscribeCredentialOwner } from "../lib/api";
import { defaultManager, onDefaultManager, useKnownDefault } from "../lib/manager-choice";
import { checkTaskCompletion, completionCheckError, type TaskDoneCheck } from "../lib/task-completion-check";
import type { Todo } from "../lib/todos";
import { Button, Loading } from "../ui/atoms";
import { InfoTip } from "../ui/InfoTip";

export interface TaskCompletionCheckDeps {
  /** Execute the completion check (defaults to `lib/task-completion-check.ts`'s `checkTaskCompletion`). */
  checkTaskCompletion?: (id: string, manager: string, signal: AbortSignal) => Promise<TaskDoneCheck>;
  /** Format check failures for display (defaults to `completionCheckError`). */
  formatError?: (error: unknown) => string;
  /** Current credential owner value or getter (bypasses `api.credentialOwner` when supplied). */
  credentialOwner?: string | (() => string);
  /** Subscribe to credential owner updates (bypasses `api.subscribeCredentialOwner` when supplied or when `credentialOwner` is supplied). */
  subscribeCredentialOwner?: (listener: () => void) => () => void;
  /** Imperative credential-change listener to retire in-flight requests immediately. */
  onCredentialChange?: (listener: () => void) => () => void;
  /** Default manager value or getter when `todo.manager` is absent (bypasses `manager-choice` when supplied). */
  defaultManager?: string | (() => string);
  /** Subscribe to default-manager updates (bypasses `useKnownDefault` when supplied or when `defaultManager` is supplied). */
  subscribeDefaultManager?: (listener: () => void) => () => void;
  /** Imperative default-manager listener to retire in-flight requests immediately. */
  onDefaultManager?: (listener: () => void) => () => void;
  /** Language tag or getter for timestamp formatting (defaults to `i18n.currentLanguage`). */
  language?: string | (() => string);
  /** Clock factory for result timestamps (defaults to `() => new Date()`). */
  now?: () => Date;
}

export interface TaskCompletionCheckProps {
  todo: Todo;
  deps?: TaskCompletionCheckDeps;
  /** Compact single-row layout under todos_v2 (#17938): moves explanatory prose into an InfoTip. */
  compact?: boolean;
}

export interface TaskCompletionCheckViewProps {
  supervised: boolean;
  busy: boolean;
  error?: string;
  result?: { check: TaskDoneCheck; at: Date };
  language?: string;
  compact?: boolean;
  onRun: () => void;
}

const noopSubscribe = () => () => {};
const subscribeDefaultMgr = (listener: () => void) => onDefaultManager(() => listener());
const defaultNow = () => new Date();

/** Each check belongs to one task, manager and login, including when an editor stays mounted across pairing. */
export function TaskCompletionCheck({ todo, deps, compact }: TaskCompletionCheckProps) {
  const controlledOwner = deps?.credentialOwner !== undefined || deps?.subscribeCredentialOwner !== undefined || deps?.onCredentialChange !== undefined;
  return controlledOwner
    ? <ControlledOwnerBridge todo={todo} deps={deps!} compact={compact} />
    : <DefaultOwnerBridge todo={todo} deps={deps} compact={compact} />;
}

interface OwnerResolvedProps {
  todo: Todo;
  deps?: TaskCompletionCheckDeps;
  compact?: boolean;
  owner: string;
  readOwner: () => string;
  listenCredential: (retire: () => void) => () => void;
}

function DefaultOwnerBridge({ todo, deps, compact }: TaskCompletionCheckProps) {
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, credentialOwner);
  return <ManagerBridge todo={todo} deps={deps} compact={compact} owner={owner} readOwner={credentialOwner} listenCredential={onCredentialChange} />;
}

function ControlledOwnerBridge({ todo, deps, compact }: { todo: Todo; deps: TaskCompletionCheckDeps; compact?: boolean }) {
  const credentialDep = deps.credentialOwner;
  const readOwner = useCallback(
    () => (typeof credentialDep === "function" ? credentialDep() : typeof credentialDep === "string" ? credentialDep : credentialOwner()),
    [credentialDep],
  );
  const onCred = deps.onCredentialChange;
  const subCred = deps.subscribeCredentialOwner;
  const subOwner = subCred ?? onCred ?? (credentialDep === undefined ? subscribeCredentialOwner : noopSubscribe);
  const owner = useSyncExternalStore(subOwner, readOwner, readOwner);
  const listenCredential = useCallback((retire: () => void) => {
    if (onCred) return onCred(retire);
    if (subCred) return subCred(() => { if (readOwner() !== owner) retire(); });
    return credentialDep === undefined ? onCredentialChange(retire) : () => {};
  }, [onCred, subCred, readOwner, owner, credentialDep]);
  return <ManagerBridge todo={todo} deps={deps} compact={compact} owner={owner} readOwner={readOwner} listenCredential={listenCredential} />;
}

function ManagerBridge(props: OwnerResolvedProps) {
  const { deps } = props;
  const controlledManager = deps?.defaultManager !== undefined || deps?.subscribeDefaultManager !== undefined || deps?.onDefaultManager !== undefined;
  return controlledManager
    ? <ControlledManagerBridge {...props} deps={deps!} />
    : <DefaultManagerBridge {...props} />;
}

function DefaultManagerBridge({ todo, deps, compact, owner, readOwner, listenCredential }: OwnerResolvedProps) {
  const knownDefault = useKnownDefault();
  const manager = todo.manager || knownDefault || defaultManager();
  return <CompletionCheck
    key={JSON.stringify([todo.id, todo.manager, manager, owner])}
    todo={todo} manager={manager} owner={owner}
    readOwner={readOwner} readManager={defaultManager}
    listenCredential={listenCredential} listenManager={onDefaultManager}
    deps={deps} compact={compact}
  />;
}

function ControlledManagerBridge({ todo, deps, compact, owner, readOwner, listenCredential }: OwnerResolvedProps & { deps: TaskCompletionCheckDeps }) {
  const managerDep = deps.defaultManager;
  const readManager = useCallback(
    () => (typeof managerDep === "function" ? managerDep() : typeof managerDep === "string" ? managerDep : defaultManager()),
    [managerDep],
  );
  const onMgr = deps.onDefaultManager;
  const subMgr = deps.subscribeDefaultManager;
  const subManager = subMgr ?? onMgr ?? (managerDep === undefined ? subscribeDefaultMgr : noopSubscribe);
  const knownDefault = useSyncExternalStore(subManager, readManager, readManager);
  const manager = todo.manager || knownDefault;
  const listenManager = useCallback((notify: () => void) => {
    if (onMgr) return onMgr(notify);
    if (subMgr) return subMgr(notify);
    return managerDep === undefined ? onDefaultManager(notify) : () => {};
  }, [onMgr, subMgr, managerDep]);
  return <CompletionCheck
    key={JSON.stringify([todo.id, todo.manager, manager, owner])}
    todo={todo} manager={manager} owner={owner}
    readOwner={readOwner} readManager={readManager}
    listenCredential={listenCredential} listenManager={listenManager}
    deps={deps} compact={compact}
  />;
}

interface CompletionCheckProps {
  todo: Todo;
  manager: string;
  owner: string;
  readOwner: () => string;
  readManager: () => string;
  listenCredential: (retire: () => void) => () => void;
  listenManager: (notify: () => void) => () => void;
  deps?: TaskCompletionCheckDeps;
  compact?: boolean;
}

function CompletionCheck({ todo, manager, owner, readOwner, readManager, listenCredential, listenManager, deps, compact }: CompletionCheckProps) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [result, setResult] = useState<{ check: TaskDoneCheck; at: Date }>();
  const pending = useRef<AbortController | null>(null);
  const unmounted = useRef(false);
  const seqRef = useRef(0);
  const readOwnerRef = useRef(readOwner);
  const readManagerRef = useRef(readManager);
  useLayoutEffect(() => {
    readOwnerRef.current = readOwner;
    readManagerRef.current = readManager;
  }, [readOwner, readManager]);

  useLayoutEffect(() => {
    unmounted.current = false;
    return () => {
      unmounted.current = true;
      ++seqRef.current;
      pending.current?.abort();
      pending.current = null;
    };
  }, []);

  useLayoutEffect(() => {
    const invalidate = () => {
      ++seqRef.current;
      const active = pending.current;
      pending.current = null;
      active?.abort();
      if (!unmounted.current) {
        setBusy(false);
        setError(undefined);
        setResult(undefined);
      }
    };
    const stop = listenCredential(invalidate);
    const stopManager = listenManager(() => { if (!todo.manager && readManagerRef.current() !== manager) invalidate(); });
    return () => { stop(); stopManager(); };
  }, [listenCredential, listenManager, manager, todo.manager]);

  const run = async () => {
    if (unmounted.current || pending.current || owner !== readOwnerRef.current()) return;
    if (manager !== (todo.manager || readManagerRef.current())) return;
    const request = new AbortController();
    const runSeq = ++seqRef.current;
    pending.current = request; // Admit synchronously, before React can render a disabled button.
    const current = () => !unmounted.current && pending.current === request && seqRef.current === runSeq
      && owner === readOwnerRef.current() && manager === (todo.manager || readManagerRef.current());
    setBusy(true);
    setError(undefined);
    setResult(undefined);
    const runCheck = deps?.checkTaskCompletion ?? checkTaskCompletion;
    const formatError = deps?.formatError ?? completionCheckError;
    const now = deps?.now ?? defaultNow;
    try {
      const check = await runCheck(todo.id, manager, request.signal);
      if (current()) setResult({ check, at: now() });
    } catch (e) {
      if (current()) setError(formatError(e));
    } finally {
      if (current()) { pending.current = null; setBusy(false); }
    }
  };

  const language = typeof deps?.language === "function" ? deps.language() : deps?.language;
  return <TaskCompletionCheckView supervised={!!todo.supervision} busy={busy} error={error} result={result} language={language} compact={compact} onRun={() => void run()} />;
}

/** Pure presentational view for the task completion check panel. */
export function TaskCompletionCheckView({ supervised, busy, error, result, language, compact = false, onRun }: TaskCompletionCheckViewProps) {
  if (compact) {
    const explanation = supervised
      ? `${t("todos.check_explanation")} ${t("todos.check_supervised")}`
      : t("todos.check_explanation");
    return (
      <section
        aria-label={t("todos.check_section")}
        aria-busy={busy}
        className="mt-3 min-w-0 rounded-control border border-line bg-canvas px-3 py-2"
        data-testid="task-completion-check"
        data-compact="true"
      >
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span className="inline-flex min-w-0 items-center gap-1.5">
            <span className="text-label font-medium text-ink-2">{t("todos.check_section")}</span>
            <InfoTip name={t("todos.check_section")} text={explanation} />
          </span>
          <Button tone="secondary" size="sm" icon="check" disabled={busy} testId="task-completion-check-run" onClick={onRun}>
            {t(error ? "todos.check_retry" : result ? "todos.check_again" : "todos.check_action")}
          </Button>
        </div>
        {busy && <div className="mt-2.5"><Loading label={t("todos.check_wait")} /></div>}
        {error && <p role="alert" className="mt-2.5 text-meta leading-[1.6] text-red">{error}</p>}
        {result && <CompletionCheckResult check={result.check} at={result.at} supervised={supervised} language={language} />}
      </section>
    );
  }
  return (
    <section aria-label={t("todos.check_section")} aria-busy={busy} className="mt-4 min-w-0 rounded-control bg-inset p-3.5 shadow-hairline" data-testid="task-completion-check">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-[12.5px] font-semibold text-ink-2">{t("todos.check_section")}</span>
        <Button tone="secondary" icon="check" disabled={busy} onClick={onRun}>
          {t(error ? "todos.check_retry" : result ? "todos.check_again" : "todos.check_action")}
        </Button>
      </div>
      <p className="mt-2 text-[12.5px] leading-[1.6] text-ink-3">{t("todos.check_explanation")}</p>
      {supervised && <p className="mt-2 text-[12.5px] leading-[1.6] text-ink-2">{t("todos.check_supervised")}</p>}
      {busy && <div className="mt-3"><Loading label={t("todos.check_wait")} /></div>}
      {error && <p role="alert" className="mt-3 text-[13px] leading-[1.6] text-red">{error}</p>}
      {result && <CompletionCheckResult check={result.check} at={result.at} supervised={supervised} language={language} />}
    </section>
  );
}

const verdictKeys: Record<string, string> = {
  done: "todos.check_done", not_done: "todos.check_not_done", waiting: "todos.check_waiting",
  needs_user: "todos.check_needs_user", unsure: "todos.check_unsure", undelivered: "todos.check_undelivered",
};
const skipKeys: Record<string, string> = {
  not_a_task: "todos.check_skip_manual", done: "todos.check_skip_done", reopened: "todos.check_skip_reopened",
};
const deliveryKeys: Record<string, string> = {
  live: "todos.check_delivery_live", none: "todos.check_delivery_none", pr: "todos.delivery_pr",
  merged: "todos.delivery_merged", off: "todos.delivery_off",
};
const unblockKeys: Record<string, string> = {
  user: "todos.unblock_user", agent: "todos.unblock_agent", other: "todos.unblock_other", supervisor: "todos.unblock_supervisor",
};

const labelKey = (keys: Record<string, string>, code: string, fallback: string) => Object.hasOwn(keys, code) ? keys[code] : fallback;

/** What applying the check did (#4867): every done verdict enters supervisor review; a judge never closes the task
 *  (#5732). Any other verdict is only kept on the task. The second argument stays for older callers. */
const appliedKey = (check: TaskDoneCheck, _supervised: boolean) =>
  check.verdict !== "done" ? "todos.check_applied_kept" : "todos.check_applied_review";

export function CompletionCheckResult({ check, at, supervised = false, language }: { check: TaskDoneCheck; at: Date; supervised?: boolean; language?: string }) {
  const delivery = check.delivery;
  return (
    <div role="status" className="mt-3 min-w-0 space-y-2 text-[13px] leading-[1.6] text-ink-2 break-words" data-testid="completion-check-result">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <strong className="text-ink">{t(labelKey(verdictKeys, check.verdict, "todos.check_unknown"))}</strong>
        <time dateTime={at.toISOString()} className="text-[12px] text-ink-3">{t("todos.check_at", { time: at.toLocaleString(language ?? currentLanguage()) })}</time>
      </div>
      <p className="whitespace-pre-wrap select-text">{check.reason || t("todos.check_no_reason")}</p>
      <p className="text-[12.5px] text-ink-3">{check.skip
        ? t(labelKey(skipKeys, check.skip, "todos.check_skip_unknown"))
        : check.applied ? t(appliedKey(check, supervised))
        : t(check.verdict === "done" && check.would_close ? "todos.check_would_close" : "todos.check_would_not_close")}</p>
      {delivery && <div className="space-y-1">
        {/* Who can clear it reads as the list row does ("Stopped at a PR · needs your OK"); an unknown owner is a sentence of its own. */}
        <p>{t("todos.check_delivery", { state: t(labelKey(deliveryKeys, delivery.state, "todos.check_delivery_unknown")) })}
          {delivery.unblock && Object.hasOwn(unblockKeys, delivery.unblock) && <> · {t(unblockKeys[delivery.unblock])}</>}</p>
        {delivery.gate && <p className="whitespace-pre-wrap select-text">{t("todos.check_blocker", { gate: delivery.gate })}</p>}
        {delivery.unblock && !Object.hasOwn(unblockKeys, delivery.unblock) && <p>{t("todos.check_unblock_unknown")}</p>}
      </div>}
    </div>
  );
}
