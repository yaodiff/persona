// The alert sheet's memory, CPU and load section (#8037): the numbers; the largest process groups the node names (by
// memory, or by CPU for CPU and load) and the Agents on this computer by what they are doing; then Stop idle Agents
// (after a confirmation that lists each one; working and needs-you Agents are never in it) and Hand work to another
// computer. Without the node's diagnosis the Agents and both actions still work.
import { useMemo, useState } from "react";
import { t } from "../../i18n";
import { sessionTitle, activityOf, stateOf } from "../../lib/format";
import { stateName } from "../../lib/state-labels";
import { bytesBin, load as loadText, percent, uptime, usedOfTotal } from "../../lib/machine-detail";
import { loadDefinition, perCoreLabel, readings, type AlertKind } from "../../lib/machine-alerts";
import { stopAgents, stoppableAgents, type StopOutcome } from "../../lib/machine-handoff";
import type { Diagnosis, ProcessGroup } from "../../lib/machine-diagnosis";
import { refresh } from "../../lib/store";
import type { Machine, MachineMetrics, Session } from "../../lib/types";
import { Button, cx, Dialog, StatePill, useNow } from "../../ui/atoms";
import { DiagnosisLoading, DiagnosisNote, Figure, SheetPart, ShareBar, useDiagnosis } from "./AlertParts";

const SHOWN = 5;
const SECTIONS = ["processes"] as const;

export function PressureSection({ kind, machine, metrics, agents, agentsKnown, redeploy, onHandoff }: {
  kind: Extract<AlertKind, "memory" | "cpu" | "load">;
  machine: Machine;
  metrics?: MachineMetrics;
  agents: readonly Session[];
  agentsKnown: "yes" | "loading" | "failed";
  /** Redeploy for a computer whose Persona is too old to say what uses it (shown only when the hub can deploy). */
  redeploy?: React.ReactNode;
  onHandoff: () => void;
}) {
  const r = readings(machine, metrics);
  const latest = metrics?.latest;
  const total = latest?.mem_total || metrics?.system?.memory || machine.system?.memory;
  const { read, reload } = useDiagnosis(machine.id, SECTIONS, machine.online);
  const byCpu = kind !== "memory";
  const tone = (v: number | undefined, warn: number, crit: number) => (v === undefined ? undefined : v >= crit ? "critical" as const : v >= warn ? "warning" as const : undefined);
  return (
    <>
      <SheetPart part="what">
        <div className="grid grid-cols-3 gap-3" data-testid="pressure-figures">
          <Figure label={t("machines-alerts.memory")} value={percent(r.memory)} detail={usedOfTotal(latest?.mem_used, total)} tone={tone(r.memory, 80, 90)} />
          <Figure label="CPU" value={percent(r.cpu)} detail={r.cores ? t("machines-alerts.cores", { count: r.cores }) : undefined} tone={tone(r.cpu, 75, 90)} />
          <Figure label={t("machines-alerts.load")} value={loadText(latest?.load)} detail={r.load !== undefined ? perCoreLabel(r.load) : undefined} tone={tone(r.load, 1, 2)} />
        </div>
        {/* the load alert says what it counts before anything else (#12519): high load with CPU to spare is queuing */}
        {kind === "load" && <p className="mt-2 text-meta leading-relaxed text-ink-2" data-testid="load-definition">{loadDefinition()}</p>}
      </SheetPart>
      <SheetPart part="why">
        <h4 className="mb-1 text-meta font-medium text-ink">{byCpu ? t("machines-alerts.processes_cpu") : t("machines-alerts.processes_memory")}</h4>
        {read.state === "loading" && <DiagnosisLoading label={t("machines-alerts.processes_loading")} />}
        {read.state === "failed" && <DiagnosisNote failure={read.failure} onRetry={reload} redeploy={redeploy} />}
        {read.state === "ok" && <ProcessList data={read.data} byCpu={byCpu} cores={r.cores} />}
        <AgentGroups agents={agents} known={agentsKnown} />
      </SheetPart>
      <SheetPart part="do">
        <PressureActions agents={agents} known={agentsKnown} onHandoff={onHandoff} machine={machine} />
      </SheetPart>
    </>
  );
}

const KIND_LABEL: Record<string, () => string> = {
  agent: () => t("machines-alerts.kind_agent"),
  engine: () => t("machines-alerts.kind_engine"),
  persona: () => t("machines-alerts.kind_persona"),
};

function ProcessList({ data, byCpu, cores }: { data: Diagnosis; byCpu: boolean; cores?: number }) {
  const [all, setAll] = useState(false);
  const p = data.processes;
  const groups = useMemo(() => {
    const list = [...(p?.groups ?? [])];
    if (byCpu) list.sort((a, b) => (b.cpu ?? -1) - (a.cpu ?? -1) || b.rss - a.rss);
    return list;
  }, [p, byCpu]);
  if (!p || !groups.length) return <p className="py-1 text-meta text-ink-3">{t("machines-alerts.processes_none")}</p>;
  const shown = all ? groups : groups.slice(0, SHOWN);
  const share = (g: ProcessGroup) => (byCpu ? (g.cpu ?? 0) / (100 * (cores || 1)) : p.mem_total ? g.rss / p.mem_total : 0);
  return (
    <div data-testid="process-groups">
      <ul className="divide-y divide-line">
        {shown.map((g) => (
          <li key={g.name} className="py-1.5" data-process={g.name}>
            <div className="flex items-baseline gap-2 text-meta">
              <span className="min-w-0 truncate font-medium text-ink">{g.name}</span>
              <span className="shrink-0 rounded-chip bg-field px-1.5 text-submeta text-ink-2">{(KIND_LABEL[g.kind] ?? (() => t("machines-alerts.kind_misc")))()}</span>
              {g.count > 1 && <span className="shrink-0 text-submeta text-ink-3 tabular-nums">{t("machines-alerts.process_count", { count: g.count })}</span>}
              <span className="ml-auto shrink-0 text-ink-2 tabular-nums">
                {byCpu ? `${percent(g.cpu)} · ${bytesBin(g.rss)}` : `${bytesBin(g.rss)} · ${percent(share(g) * 100)}`}
              </span>
            </div>
            <div className="mt-1">
              <ShareBar share={share(g)} />
            </div>
          </li>
        ))}
      </ul>
      <div className="mt-1 flex items-center gap-2 text-submeta text-ink-3">
        <span className="flex-1">{t("machines-alerts.processes_seen", { count: p.count })}</span>
        {groups.length > SHOWN && (
          <Button size="sm" tone="quiet" onClick={() => setAll(!all)} expanded={all}>
            {all ? t("machines-alerts.show_fewer") : t("machines-alerts.show_all", { count: groups.length })}
          </Button>
        )}
      </div>
    </div>
  );
}

type Group = "needs_you" | "working" | "idle";
const groupOf = (s: Session): Group => {
  const st = activityOf(s);
  return st === "needs_you" ? "needs_you" : st === "working" ? "working" : "idle";
};

/** The computer's Agents: needs you, working, idle (with how long), as the native cards count them. */
export function AgentGroups({ agents, known }: { agents: readonly Session[]; known: "yes" | "loading" | "failed" }) {
  const now = useNow(60_000);
  if (known !== "yes") return <p className="mt-3 text-meta text-ink-3">{known === "loading" ? t("machines-alerts.agents_loading") : t("machines-alerts.agents_failed")}</p>;
  const groups: Record<Group, Session[]> = { needs_you: [], working: [], idle: [] };
  for (const s of agents) groups[groupOf(s)].push(s);
  const heading: Record<Group, string> = { needs_you: stateName("needs_you"), working: stateName("working"), idle: t("machines-alerts.agents_idle") };
  return (
    <div className="mt-3" data-testid="agent-groups">
      <h4 className="mb-1 text-meta font-medium text-ink">{t("machines-alerts.agents_title", { count: agents.length })}</h4>
      {!agents.length && <p className="text-meta text-ink-3">{t("machines-alerts.agents_none")}</p>}
      {(["needs_you", "working", "idle"] as Group[]).filter((g) => groups[g].length).map((g) => (
        <div key={g} className="mt-1.5" data-group={g}>
          <div className="text-submeta text-ink-3">{heading[g]} · {groups[g].length}</div>
          <ul>
            {groups[g].map((s) => {
              const since = Date.parse(s.state_since || s.updated_at || "");
              return (
                <li key={s.session_id} className="flex items-center gap-2 py-1 text-meta">
                  <span className="min-w-0 flex-1 truncate text-ink">{sessionTitle(s)}</span>
                  {g === "idle" && Number.isFinite(since) && <span className="shrink-0 text-submeta text-ink-3 tabular-nums">{t("machines-alerts.idle_for", { time: uptime(since / 1000, now / 1000) })}</span>}
                  <StatePill state={stateOf(s)} />
                </li>
              );
            })}
          </ul>
        </div>
      ))}
    </div>
  );
}

/** Stop idle Agents and Hand work to another computer, with the stop's confirmation and its outcome. */
function PressureActions({ agents, known, onHandoff, machine }: { agents: readonly Session[]; known: "yes" | "loading" | "failed"; onHandoff: () => void; machine: Machine }) {
  const idle = useMemo(() => stoppableAgents(agents), [agents]);
  const [asking, setAsking] = useState<Session[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<StopOutcome[] | null>(null);
  const others = agents.length > 0;
  const stop = async () => {
    const list = asking ?? [];
    setBusy(true);
    try {
      const out = await stopAgents(list);
      setOutcome(out);
      setAsking(null);
      await refresh().catch(() => {});
    } finally {
      setBusy(false);
    }
  };
  const failed = outcome?.filter((o) => !o.ok) ?? [];
  return (
    <>
      <div className="flex flex-wrap gap-2">
        <Button tone="secondary" icon="stop" disabled={known !== "yes" || !idle.length || !machine.online} onClick={() => setAsking(idle)} testId="stop-idle">
          {idle.length ? t("machines-alerts.stop_idle_count", { count: idle.length }) : t("machines-alerts.stop_idle")}
        </Button>
        <Button tone="secondary" icon="forward" disabled={known !== "yes" || !others} onClick={onHandoff} testId="handoff">
          {t("machines-alerts.handoff")}
        </Button>
      </div>
      <p className="mt-1.5 text-submeta text-ink-3">{known === "yes" && !idle.length ? t("machines-alerts.stop_idle_none") : t("machines-alerts.stop_idle_note")}</p>
      {outcome && (
        <div className={cx("mt-2 rounded-control px-3 py-2 text-meta", failed.length ? "bg-red-tint" : "bg-green-tint")} role="status" data-testid="stop-outcome">
          <p className={failed.length ? "text-red" : "text-green"}>{t("machines-alerts.stopped", { count: outcome.length - failed.length, total: outcome.length })}</p>
          {failed.map((f) => (
            <p key={f.session.session_id} className="mt-0.5 break-words text-ink-2">{t("machines-alerts.stop_failed_item", { title: sessionTitle(f.session), error: f.error ?? "" })}</p>
          ))}
        </div>
      )}
      <Dialog
        open={!!asking}
        onClose={() => !busy && setAsking(null)}
        title={t("machines-alerts.stop_confirm_title", { count: asking?.length ?? 0 })}
        footer={
          <>
            <Button tone="quiet" disabled={busy} onClick={() => setAsking(null)}>
              {t("common.cancel")}
            </Button>
            <Button tone="danger" busy={busy} onClick={() => void stop()} testId="stop-confirm">
              {t("machines-alerts.stop_confirm", { count: asking?.length ?? 0 })}
            </Button>
          </>
        }
      >
        <p className="text-meta leading-relaxed text-ink-2">{t("machines-alerts.stop_confirm_body", { machine: machine.name })}</p>
        <ul className="mt-2 space-y-1" data-testid="stop-list">
          {asking?.map((s) => (
            <li key={s.session_id} className="flex items-center gap-2 text-meta">
              <span className="min-w-0 flex-1 truncate text-ink">{sessionTitle(s)}</span>
              <StatePill state={stateOf(s)} />
            </li>
          ))}
        </ul>
      </Dialog>
    </>
  );
}
