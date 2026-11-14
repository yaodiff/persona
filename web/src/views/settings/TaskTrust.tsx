import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { t } from "../../i18n";
import { ApiError, credentialOwner, isAbort } from "../../lib/api";
import { shownManager } from "../../lib/manager-choice";
import {
  captureTaskTrustScope, emitTaskTrust, listTaskTrust, revokeTaskTrust, subscribeTaskTrustContext,
  taskTrustContext, taskTrustLedger, type TaskTrustRule, type TrustFailure,
} from "../../lib/task-trust";
import { Button, Dialog, Loading } from "../../ui/atoms";
import { Row } from "./SettingsParts";

const word = (key: string) => t(`settings-assistantsettings.trust_${key}`);

export function TaskTrust({ manager, machineName }: { manager: string; machineName: (id: string) => string }) {
  const context = useSyncExternalStore(subscribeTaskTrustContext, taskTrustContext, taskTrustContext);
  return <TaskTrustScope key={JSON.stringify([manager, context])} manager={manager} context={context} machineName={machineName} />;
}

function TaskTrustScope({ manager, context, machineName }: { manager: string; context: string; machineName: (id: string) => string }) {
  const [open, setOpen] = useState(false);
  return <>
    <Row label={word("title")} anchor="task-trust">
      <Button className="pointer-coarse:min-h-11" onClick={() => setOpen((value) => !value)}>{word(open ? "hide" : "manage")}</Button>
    </Row>
    {open && <TaskTrustList manager={manager} context={context} machineName={machineName} />}
  </>;
}

function readProblem(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 403) return word("read_forbidden");
    if (error.status === 404) return word("read_not_found");
    if (error.status === 503) return word("read_unavailable");
  }
  return word("read_error");
}

function revokeProblem(error: unknown): TrustFailure {
  if (error instanceof ApiError) {
    if (error.status === 403) return "forbidden";
    if (error.status === 404) return "not_found";
    if (error.status === 503) return "unavailable";
  }
  return "unconfirmed";
}

function RuleDetails({ rule, machineName }: { rule: TaskTrustRule; machineName: (id: string) => string }) {
  return <dl className="min-w-0 space-y-1 text-sm text-ink-2">
    {rule.ambiguous ? <>
      <div>{word("unresolved")}</div>
      <div><dt className="inline font-medium">{word("identifier")}: </dt><dd className="inline whitespace-pre-wrap break-all">{rule.key}</dd></div>
    </> : <>
      <div><dt className="inline font-medium">{word("machine")}: </dt><dd className="inline whitespace-pre-wrap break-all">{machineName(rule.machine)}</dd></div>
      <div><dt className="inline font-medium">{word("directory")}: </dt><dd className="inline whitespace-pre-wrap break-all">{rule.cwd}</dd></div>
    </>}
    <div><dt className="inline font-medium">{word("access")}: </dt><dd className="inline">{word(rule.readonly ? "read" : "edit")}</dd></div>
  </dl>;
}

function TaskTrustList({ manager, context, machineName }: { manager: string; context: string; machineName: (id: string) => string }) {
  const [scope] = useState(() => captureTaskTrustScope(manager));
  const [ledger] = useState(() => taskTrustLedger(scope));
  const [data, setData] = useState<{ rows: TaskTrustRule[]; revision: number } | null>(null);
  const [problem, setProblem] = useState("");
  const [reading, setReading] = useState(!ledger.pending);
  const [, setRevision] = useState(ledger.revision);
  const [intent, setIntent] = useState<TaskTrustRule | null>(null);
  const intentRef = useRef<TaskTrustRule | null>(null);
  const life = useRef({ live: false, seq: 0, reading: false, controller: null as AbortController | null });
  const current = () => life.current.live && !ledger.retired && context === taskTrustContext() && shownManager() === manager && credentialOwner() === scope.owner;
  const changeIntent = (rule: TaskTrustRule | null) => { intentRef.current = rule; setIntent(rule); };

  const read = async () => {
    const l = life.current;
    if (!current() || ledger.pending) return;
    changeIntent(null);
    const seq = ++l.seq;
    const revision = ledger.revision;
    l.controller?.abort();
    const controller = new AbortController(); l.controller = controller;
    l.reading = true; setReading(true); setProblem("");
    try {
      const rows = await listTaskTrust(scope, controller.signal, () => current() && seq === l.seq && revision === ledger.revision);
      if (current() && seq === l.seq && revision === ledger.revision) setData({ rows, revision });
    } catch (error) {
      if (current() && seq === l.seq && revision === ledger.revision && !isAbort(error)) setProblem(readProblem(error));
    } finally {
      if (current() && seq === l.seq) { l.reading = false; setReading(false); }
    }
  };

  useEffect(() => {
    const l = life.current;
    l.live = true;
    const retire = subscribeTaskTrustContext(() => {
      if (!current()) { l.seq++; l.controller?.abort(); }
    });
    const changed = (settled: boolean) => {
      if (!current()) return;
      setRevision(ledger.revision);
      if (settled) void read();
      else { l.seq++; l.controller?.abort(); l.reading = false; setReading(false); }
    };
    ledger.listeners.add(changed);
    void read();
    return () => { l.live = false; l.seq++; l.controller?.abort(); ledger.listeners.delete(changed); retire(); };
    // The keyed scope owns this lifetime, even when the parent redraws.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const begin = (rule: TaskTrustRule) => {
    if (!current() || ledger.pending || life.current.reading) return;
    changeIntent(rule);
  };
  const dismissFailure = (rule: TaskTrustRule) => {
    if (!current() || ledger.pending || !ledger.failed.has(rule.key)) return;
    changeIntent(null);
    ledger.failed.delete(rule.key);
    emitTaskTrust(ledger, false);
  };
  const revoke = async (rule: TaskTrustRule) => {
    if (!current() || ledger.pending || life.current.reading || intentRef.current !== rule) return;
    ledger.pending = rule;
    emitTaskTrust(ledger, false); // Admission is synchronous, including across remounts.
    let failure: TrustFailure | null = null;
    try { await revokeTaskTrust(scope, rule.key, current); }
    catch (error) { failure = revokeProblem(error); }
    if (ledger.retired || credentialOwner() !== scope.owner) return;
    ledger.pending = null;
    if (failure) ledger.failed.set(rule.key, { rule, reason: failure });
    else {
      ledger.failed.delete(rule.key);
      ledger.removed.set(rule.key, ledger.revision + 1);
    }
    if (current()) changeIntent(null);
    emitTaskTrust(ledger, true);
  };

  const visible = new Map<string, TaskTrustRule>();
  for (const row of data?.rows ?? []) {
    if ((ledger.removed.get(row.key) ?? 0) <= (data?.revision ?? 0)) visible.set(row.key, row);
  }
  for (const failure of ledger.failed.values()) visible.set(failure.rule.key, failure.rule);
  if (ledger.pending) visible.set(ledger.pending.key, ledger.pending);

  return <>
    <div data-task-trust-list className="flex flex-col gap-3 px-4 py-3 text-sm text-ink-2">
      <p>{word("intro")}</p>
      {!data && reading && <Loading />}
      {problem && <p role="alert" className="text-red">{problem}</p>}
      {data && visible.size === 0 && <p>{word("empty")}</p>}
      {[...visible.values()].map((rule) => <div key={rule.key} data-task-trust-rule className="flex min-w-0 flex-col items-start gap-2 border-t border-line pt-3">
        <RuleDetails rule={rule} machineName={machineName} />
        {ledger.failed.has(rule.key) && <>
          <p role="alert" className="text-red">{word(`revoke_${ledger.failed.get(rule.key)!.reason}`)}</p>
          <p>{word("dismiss_explanation")}</p>
        </>}
        <div className="flex flex-wrap gap-2">
          <Button className="pointer-coarse:min-h-11" disabled={reading || !!ledger.pending} onClick={() => begin(rule)}>
            {word(ledger.failed.has(rule.key) ? "retry_revoke" : "revoke")}
            <span className="sr-only"> {rule.ambiguous ? rule.key : `${machineName(rule.machine)} ${rule.cwd}`} {word(rule.readonly ? "read" : "edit")}</span>
          </Button>
          {ledger.failed.has(rule.key) && <Button className="pointer-coarse:min-h-11" tone="quiet" disabled={!!ledger.pending} onClick={() => dismissFailure(rule)}>
            {word("dismiss_warning")}
            <span className="sr-only"> {rule.ambiguous ? rule.key : `${machineName(rule.machine)} ${rule.cwd}`} {word(rule.readonly ? "read" : "edit")}</span>
          </Button>}
        </div>
      </div>)}
      <div><Button className="pointer-coarse:min-h-11" tone="quiet" icon="retry" busy={reading} disabled={!!ledger.pending} onClick={() => void read()}>{word("refresh")}</Button></div>
    </div>
    <Dialog open={intent !== null} onClose={() => { if (!ledger.pending) changeIntent(null); }} title={word("confirm_title")} footer={<>
      <Button className="pointer-coarse:min-h-11" tone="quiet" disabled={!!ledger.pending} onClick={() => changeIntent(null)}>{t("common.cancel")}</Button>
      <Button className="pointer-coarse:min-h-11" tone="danger" busy={!!ledger.pending} onClick={() => { if (intent) void revoke(intent); }}>{word("confirm_button")}</Button>
    </>}>
      {intent && <div className="flex flex-col gap-3">
        <p className="text-sm text-ink-2">{word("confirm_body")}</p>
        <RuleDetails rule={intent} machineName={machineName} />
        <p className="text-sm text-ink-2">{word("scope")}</p>
      </div>}
    </Dialog>
  </>;
}
