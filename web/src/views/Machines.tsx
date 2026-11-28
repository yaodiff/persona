// Machines: every computer's CPU / memory / network now and over the last hour (docs/server/machines.md), as Insight Cards. "Add computer"
// (feature machines_add) adds one from here, and a computer waiting to be confirmed (trust pending) says so on its card.
import { memo, Suspense, useCallback, useEffect, useMemo, useRef, useState, type ComponentProps } from "react";
import { storedValue, storeValue } from "../lib/compatibility";
import { lazyPage } from "../lib/lazy-page";
import { useScrollMemory } from "../lib/scroll-memory";
import { openUsageMap, takeUsageFocus } from "../lib/usage-map-link";
import { alertLabel, concernOf, launcherFailing, loadDefinition, machineAlerts, orderMachines, type AlertKind, type LauncherHealthRecord } from "../lib/machine-alerts";
import { t as tr } from "../i18n";
import { canAddComputer, isPending, machineErrorText, neverConnected } from "../lib/add-computer";
import { api, enc, errorText } from "../lib/api";
import { readinessChanged } from "../lib/readiness";
import "./machines/machine-views.css";
import { ENGINE_SECTION_ID } from "./machines/engine-anchor";
import { openNewAgent } from "../lib/new-agent";
import { stateCount } from "../lib/state-labels";
import { activityOf } from "../lib/format";
import { hubLabel, routeText } from "../lib/mesh";
import { machineDeployment } from "../lib/machine-deploy";
import { classifyConnection } from "../lib/node-health";
import { href, navigate, onLinkClick, useRoute, type Route } from "../lib/router";
import { hostedManagerAttention, hostedManagersForMachine, offlineManagerOf, useRoster, type Manager } from "../lib/managers";
import { useMachineMetrics } from "../lib/machine-metrics";
import { onHubEvent, useStore } from "../lib/store";
import { useFullAgentSessions } from "../lib/sessions";
import { documentPage, visiblePoll } from "../lib/visible-poll";
import type { Machine, MachineMetrics, Session } from "../lib/types";
import { Button, cx, EASE, IconButton, Loading, toast } from "../ui/atoms";
import { Icon } from "../ui/icons";
import { Empty } from "./assistant/parts";
import { lazyDialog, lazyOverlay } from "./LazyOverlay";
import type { PendingComputer } from "./machines/AddComputer";
import { MachineMetricsSummary } from "./machines/MachineMetricsSummary";
import { AlertChips } from "./machines/MachineAlertChips";
import { MachineAlertSheet } from "./machines/MachineAlertSheet";
import { MachineCapabilities } from "./machines/MachineCapabilities";
import { Segmented } from "./assistant/parts";
export { Spark } from "./machines/MachineMetricsSummary";
import { useScopedMachines } from "../lib/current-assistant";
import { t } from "../i18n";
import { addTokens, formatTokens, hitRate, percentText, totalTokens, type UsageTokens } from "../lib/tokens";

const MachineDetail = lazyPage(() => import("./machines/MachineDetail").then(m => m.MachineDetail));
const ResourcesPanel = lazyPage(() => import("./machines/ResourcesPanel").then((m) => m.ResourcesPanel));

const NOOP = () => {};
type MachineDeployProps = ComponentProps<typeof import("./machines/MachineDeploy").MachineDeploy>;

// Loaded strictly on demand when the user opens Add computer or Help, or when a card has a deployment banner (no idle preload).
const AddComputer = lazyDialog(() => import("./machines/AddComputer").then((m) => m.AddComputer));
const HelpDialog = lazyOverlay(() => import("./assistant/ReadinessHelp").then((m) => m.HelpDialog));
const MachineDeploy = lazyOverlay<MachineDeployProps & { onClose: () => void }>(() =>
  import("./machines/MachineDeploy").then(({ MachineDeploy: Deploy }) => function LazyMachineDeploy(props: MachineDeployProps & { onClose: () => void }) {
    return <Deploy machine={props.machine} className={props.className} onDetails={props.onDetails} hostedManagers={props.hostedManagers} />;
  }),
);

export function Machines({ onToggleSidebar }: { onToggleSidebar: () => void }) {
  // with several assistants, the current one's computers (docs/assistant/managers.md "Clients"); one: as before
  const machines = useScopedMachines();
  const roster = useRoster();
  const loaded = useStore((s) => s.loaded);
  const canAdd = useStore((s) => canAddComputer(s.features));
  const [adding, setAdding] = useState<{ again: PendingComputer | null } | null>(null);
  // Hubs without machines_add get the plain how-to instead of the in-app flow.
  const [helping, setHelping] = useState(false);
  const [answering, setAnswering] = useState("");
  const { metrics, error } = useMachineMetrics();
  // Each computer's agents. A hub that pages its Agents list pushes no sessions, so the store holds none of them: they
  // are read as the Agents list reads them (each parent once, folded; the assistant's conversations are no agents),
  // and until that read is in, a card says so rather than "No agents running".
  const full = useFullAgentSessions();
  const agents = useMemo(() => agentCounts(full.sessions), [full.sessions]);
  const known: AgentsKnown = full.loaded ? "yes" : full.error ? "failed" : "loading";
  const route = useRoute();
  const [savedView, setView] = useState<MachinesView>(() => fleetChoice(storedValue(VIEW_KEY)));
  const choice = fleetChoice(history.state?.fleetView ?? savedView);
  const view = machines.length < 2 && choice === "capabilities" ? "cards" : choice;
  const fleetView = view !== "resources";
  const chooseView = (v: MachinesView) => {
    history.replaceState({ ...history.state, fleetView: v }, "");
    if (v !== "resources") {
      storeValue(VIEW_KEY, v);
    }
    setView(v);
  };
  const [sheet, setSheet] = useState<{ mid: string; kind: Exclude<AlertKind, "disk"> } | null>(null);
  useEffect(() => {
    if (route.name !== "machines") return;
    const req = takeUsageFocus();
    if (req) navigate({ name: "machine", mid: req.mid, subview: "storage", mode: "reclaim" }, true);
  }, [route.name]);
  const legacyEngine = route.name === "machine" && !route.subview && location.hash === "#" + ENGINE_SECTION_ID ? route.mid : null;
  useEffect(() => {
    if (legacyEngine) navigate({ name: "machine", mid: legacyEngine, subview: "management" }, true);
  }, [legacyEngine]);
  const openAlert = useCallback((mid: string, kind: AlertKind) => {
    if (kind === "disk") {
      setSheet(null);
      openUsageMap(mid);
    } else setSheet({ mid, kind });
  }, []);
  const records = useLauncherHealth();
  const issues = useMemo(() => {
    const by = new Map<string, number>();
    for (const r of records.list) if (launcherFailing(r)) by.set(r.machine, (by.get(r.machine) ?? 0) + 1);
    return by;
  }, [records.list]);
  const launcherIssues = useCallback((mid: string) => issues.get(mid) ?? 0, [issues]);
  // Co-located Manager assistants per machine, computed once per roster/machine change.
  const hostedByMachine = useMemo(() => {
    const map = new Map<string, readonly Manager[]>();
    if (!roster) return map;
    for (const m of machines) {
      const list = hostedManagersForMachine(roster, m);
      if (list.length) map.set(m.id, list);
    }
    return map;
  }, [roster, machines]);

  // the cards with something wrong first, as the apps order them (MachineConcern)
  const ordered = useMemo(() => orderMachines(machines, (m) => {
    const mm = metrics?.find((x) => x.machine_id === m.id);
    return concernOf(m, machineAlerts({ machine: m, metrics: mm, launchers: issues.get(m.id) ?? 0, managerOffline: !!offlineManagerOf(roster, m) }), agents.get(m.id)?.needsYou ?? 0);
  }), [machines, metrics, issues, roster, agents]);

  // Stable callbacks, so the memoized cards keep their props when the page renders again.
  const reconnect = useCallback(async (id: string): Promise<void> => {
    try {
      await api(`/machines/${encodeURIComponent(id)}/reconnect`, { method: "POST" });
      toast(t("machines.reconnecting"), "ok");
    } catch (e) {
      toast(t("machines.reconnect_failed", { error: errorText(e) }), "error");
    }
  }, []);

  // It is mine / Not mine for a computer waiting to be confirmed; the hub's next machines push shows the result.
  const answer = useCallback(async (m: { id: string; name: string }, what: "confirm" | "reject") => {
    setAnswering(m.id);
    try {
      await api(`/machines/${enc(m.id)}/${what}`, { method: "POST" });
      toast(t(what === "confirm" ? "machines.confirmed" : "machines.rejected", { name: m.name }), "ok");
      void readinessChanged();
    } catch (e) {
      toast(t("machines.answer_failed", { error: errorText(e) }), "error");
    } finally {
      setAnswering("");
    }
  }, []);
  const addAgain = useCallback((again: PendingComputer) => setAdding({ again }), []);

  if (route.name === "machine") {
    return <div className="@container h-full">
      <div className="flex h-full min-w-0">
        {machines.length > 1 && <nav aria-label={t("machines.title")} className="hidden w-52 shrink-0 overflow-y-auto border-r border-line p-2 @3xl:block">
          <a className="flex min-h-11 items-center gap-2 px-2 text-label font-medium text-ink-2 hover:text-ink"
            href={href({ name: "machines" })} onClick={e => onLinkClick(e, { name: "machines" })}>
            <Icon name="chevronLeft" size={14} />{t("machines.title")}
          </a>
          {ordered.map(m => {
            const to = { ...route, mid: m.id, mode: undefined, path: m.id === route.mid ? route.path : undefined };
            const counts = agents.get(m.id);
            const alert = machineAlerts({ machine: m, metrics: metrics?.find(mm => mm.machine_id === m.id),
              launchers: launcherIssues(m.id), managerOffline: !!offlineManagerOf(roster, m) })[0];
            return <a key={m.id} href={href(to)} onClick={e => onLinkClick(e, to)} aria-current={m.id === route.mid ? "page" : undefined}
              className="block rounded-control px-3 py-2 text-label hover:bg-hover aria-[current=page]:bg-field">
              <span className="block truncate font-medium text-ink">{m.name}</span>
              <span className="flex items-center gap-1.5 text-submeta text-ink-2">
                <span aria-hidden className={cx("size-2 rounded-full", m.online ? "bg-green" : "bg-ink-3")} />
                {t(m.online ? "machines.online" : "machines.offline")}
              </span>
              {known === "yes" && !!counts?.needsYou && <span className="block text-submeta text-orange">{stateCount("needs_you", counts.needsYou)}</span>}
              {known === "yes" && !!counts?.working && <span className="block text-submeta text-ink-2">{stateCount("working", counts.working)}</span>}
              {alert && alert.kind !== "offline" && <span className="block text-submeta text-orange">{alertLabel(alert)}</span>}
            </a>;
          })}
        </nav>}
        <div className="min-w-0 flex-1">
          <Suspense fallback={<Loading />}><MachineDetail key={route.mid} mid={route.mid}
            subview={route.subview ?? (legacyEngine ? "management" : route.range ? "monitoring" : "work")} mode={route.mode} onToggleSidebar={onToggleSidebar} /></Suspense>
        </div>
      </div>
    </div>;
  }

  const fleetWidth = machines.length === 1 ? "max-w-3xl" : "max-w-[1560px]";
  const add = (
    <Button tone="primary" icon="plus" onClick={() => (canAdd ? setAdding({ again: null }) : setHelping(true))}>
      {t("machines.add")}
    </Button>
  );

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
        <IconButton icon="sidebar" label={t("machines.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
        <h1 className="text-[14px] font-semibold text-ink">{t("machines.title")}</h1>
        <div className="flex-1" />
        {machines.length > 0 && canAdd && add}
      </header>
      <FleetScroll view={view}>
        <div className={cx("mx-auto px-5 pt-3 sm:px-8", fleetWidth)}>
          <div className="machine-views w-fit max-w-full">
            <Segmented<MachinesView> compact wrap value={view} onChange={chooseView} label={t("machines.view_label")} options={[
              { v: "cards", label: t("machines.view_cards") },
              ...(machines.length > 1 ? [{ v: "capabilities" as const, label: t("machines.view_capabilities") }] : []),
              { v: "resources", label: t("resources.assistant_resources") },
            ]} />
          </div>
        </div>
        {/* two columns, three on a wide screen: the cards used to stop at 1000px and leave half a wide window empty; a full-row item takes col-span-full, since md:col-span-2 alone leaves a third of the row to the next card */}
        <div className={cx("mx-auto grid grid-cols-1 gap-3 px-5 py-6 sm:px-8", fleetWidth, machines.length !== 1 && "md:grid-cols-2 2xl:grid-cols-3")}>
          {fleetView && machines.length > 1 && <TodayTokens machines={machines} metrics={metrics} />}
          {view === "resources" && <div className="col-span-full"><Suspense fallback={<Loading />}><ResourcesPanel /></Suspense></div>}
          {fleetView && !metrics && !error && (!loaded || machines.length > 0) && <Loading />}
          {/* a failed refresh says so even with the last numbers on screen (iOS / Android keep their notice up) */}
          {fleetView && error && (
            <p role="alert" className="flex items-start gap-2 rounded-[8px] bg-red-tint px-3 py-2 text-[12.5px] text-red col-span-full">
              <Icon name="warning" size={14} className="mt-px shrink-0" />
              <span className="min-w-0 break-words">{metrics ? t("machines.refresh_failed_stale", { error, seconds: 10 }) : t("machines.load_failed", { error })}</span>
            </p>
          )}
          {view !== "resources" && loaded && machines.length === 0 && (
            <div className="col-span-full">
              <Empty icon="machine" title={error && !metrics ? t("machines.empty_unavailable") : t("machines.empty_title")} action={add}>
                {t("machines.empty_body_add")}
              </Empty>
            </div>
          )}
          {view === "capabilities" && machines.length > 0 && (
            <div className="col-span-full">
              <MachineCapabilities machines={machines} metrics={metrics} launcherIssues={launcherIssues} onAlert={openAlert} />
            </div>
          )}
          {view === "cards" && machines.length > 0 && (
            <p className="col-span-full text-submeta text-ink-3">{t("machines.refresh_interval", { seconds: 10 })}</p>
          )}
          {view === "cards" && ordered.map((m, index) => (
              <MachineCard
                key={m.id}
                m={m}
                onlyComputer={machines.length === 1}
                index={index}
                metrics={metrics?.find((x) => x.machine_id === m.id)}
                hub={hubLabel(m, machines)}
                route={routeText(m, machines)}
                canAdd={canAdd}
                answering={answering === m.id}
                onReconnect={reconnect}
                onAnswer={answer}
                onAddAgain={addAgain}
                managerOffline={offlineManagerOf(roster, m)?.name}
                hostedManagers={hostedByMachine.get(m.id) ?? NO_MANAGERS}
                agents={agents.get(m.id)?.total ?? 0}
                working={agents.get(m.id)?.working ?? 0}
                needsYou={known === "yes" ? agents.get(m.id)?.needsYou ?? 0 : undefined}
                waitingSid={agents.get(m.id)?.waitingSid}
                known={known}
                launcherIssues={issues.get(m.id) ?? 0}
                onAlert={openAlert}
              />
          ))}
          {/* what load counts, under the cards that show it (#12519; the apps' footnote) */}
          {view === "cards" && metrics && machines.length > 0 && (
            <p className="col-span-full px-1 text-submeta leading-relaxed text-ink-3" data-testid="load-definition">{loadDefinition()}</p>
          )}
        </div>
      </FleetScroll>
      <AlertSheetHost sheet={sheet} machines={machines} metrics={metrics} sessions={full.sessions} known={known} records={records} launcherIssues={launcherIssues} roster={roster} onKind={openAlert} onClose={() => setSheet(null)} onReconnect={reconnect} />
      <AddComputer open={!!adding} again={adding?.again} onClose={() => setAdding(null)} />
      {helping && <HelpDialog topic="add_computer" machine="" step={null} onClose={() => setHelping(false)} onAction={() => {}} />}
    </div>
  );
}

type CardProps = {
  m: Machine;
  onlyComputer: boolean;
  index: number;
  metrics: MachineMetrics | undefined;
  hub: string;
  route: string;
  canAdd: boolean;
  answering: boolean;
  onReconnect: (id: string) => void;
  onAnswer: (m: { id: string; name: string }, what: "confirm" | "reject") => Promise<void>;
  onAddAgain: (again: PendingComputer) => void;
  /** Its manager's name when that manager is offline (several managers): why it is offline, and no Reconnect. */
  managerOffline?: string;
  /** Installed Manager assistants hosted on this computer, empty when none. */
  hostedManagers: readonly Manager[];
  /** Its agents that are not offline, and how many of them work; `known`: whether the list they come from is in. */
  agents: number;
  working: number;
  needsYou?: number;
  waitingSid?: string;
  known: AgentsKnown;
  /** Launchers failing or blocked on it (docs/server/launcher-health.md). */
  launcherIssues: number;
  /** Opens the alert sheet on one of its alerts. */
  onAlert: (mid: string, kind: AlertKind) => void;
};

type AgentsKnown = "yes" | "loading" | "failed";

// One computer's card. It is memoized and gets its own computer's agents as numbers: a change to another computer's
// agents leaves its props as they were, so it (and its Stat, Spark, MachineDeploy) does not render again. Nor does a
// machines push that only moved last_seen on (sameCard).
const MachineCard = memo(function MachineCard({ m, onlyComputer, index, metrics: mm, hub, route, canAdd, answering, onReconnect, onAnswer, onAddAgain, managerOffline, hostedManagers, agents, working, needsYou, waitingSid, known, launcherIssues, onAlert }: CardProps) {
  const detail = { name: "machine" as const, mid: m.id };
  const waiting: Route = waitingSid ? { name: "session", mid: m.id, sid: waitingSid } : detail;
  const deployment = machineDeployment(m);
  const { expectedDisconnect } = deployment;
  const pending = isPending(m);
  // why it is offline: the hub's error_code in our words, else from the error text, and what to do
  const coded = pending ? "" : machineErrorText(m.error_code);
  const connFailure = expectedDisconnect || pending || coded || managerOffline ? null : classifyConnection(m);
  // what is wrong, as chips (#8037); the deployment strip below says the version's and a failed update's itself
  const alerts = machineAlerts({ machine: m, metrics: mm, launchers: launcherIssues, managerOffline: !!managerOffline }).filter((a) => a.kind !== "mismatch" && a.kind !== "deploy_failed");
  const versions = [m.claude_version && `claude ${m.claude_version}`, m.herdr_version && `herdr ${m.herdr_version}`].filter(Boolean).join(" · ");
  return (
    <section onClick={(e) => openFromCard(e, detail)} className="cursor-pointer overflow-hidden rounded-card bg-surface shadow-card card-interactive" style={{ animation: `fade-up 420ms ${EASE} ${index * 60}ms both` }}>
      <div className="flex items-start gap-3 px-4 pt-3.5">
        <span className={cx("flex size-9 shrink-0 items-center justify-center rounded-[9px]", m.online ? "bg-field text-ink-2" : "bg-inset text-ink-3")}>
          <Icon name="machine" size={16} />
        </span>
        <div className="flex min-w-0 flex-1 flex-wrap items-start gap-x-2">
          <a href={href(detail)} onClick={(e) => onLinkClick(e, detail)} className="group/title min-w-0 grow basis-48 rounded-control">
            <div className="flex items-center gap-2">
              <h2 className="truncate text-[14px] font-semibold text-ink group-hover/title:underline">{m.name}</h2>
              {hub && (
                <span title={route ? `${hub} · ${route}` : hub} className="inline-flex h-5 shrink-0 items-center gap-1 whitespace-nowrap rounded-full bg-field px-1.5 text-[11px] font-medium text-ink-2">
                  <Icon name="hub" size={11} />
                  {hub}
                </span>
              )}
              <span className={cx("inline-flex h-5 items-center gap-1 rounded-full px-1.5 text-[11px] font-medium", m.online ? "bg-green-tint text-green" : "bg-field text-ink-3")}>
                <span className={cx("size-1.5 rounded-full", m.online ? "bg-green" : "bg-ink-3")} />
                {m.online ? t("machines.online") : neverConnected(m) ? t("machines.not_connected_yet") : t("machines.offline")}
              </span>
            </div>
            <p className="truncate text-[11.5px] text-ink-3">{[m.system?.name || m.os, m.system?.model, m.system?.cpu].filter(Boolean).join(" · ") || m.hostname}</p>
          </a>
          {needsYou !== undefined && needsYou > 0 && (
            <a href={href(waiting)} onClick={(e) => onLinkClick(e, waiting)}
              className="inline-flex min-h-11 shrink-0 items-center rounded-control focus-visible:outline-accent">
              <span className="whitespace-nowrap rounded-full bg-field px-2 py-1 text-submeta font-medium text-attention">
                {stateCount("needs_you", needsYou)}
              </span>
            </a>
          )}
        </div>
        {!m.online && !expectedDisconnect && !pending && !managerOffline && (
          <Button tone="secondary" icon="retry" onClick={() => onReconnect(m.id)}>
            {t("machines.reconnect")}
          </Button>
        )}
      </div>
      {hostedManagers.length > 0 && (
        <div
          className={cx("mx-4 mt-2 flex flex-wrap items-center gap-1.5", !m.online && !alerts.length && !pending && !managerOffline && !coded && !connFailure && !m.node_mismatch && !m.deploying && !m.deploy_phase && !m.deploy_error && "mb-3")}
          data-testid="hosted-managers"
        >
          {hostedManagers.map((hm) => {
            const attention = hostedManagerAttention(hm, m.node_version);
            const warn = attention !== null;
            const statusText = attention === "pending_restart"
              ? t("machines.manager_pending_restart")
              : attention === "version_lag"
                ? t("machines.manager_version_lag")
                : hm.online ? t("machines.online") : t("machines.offline");
            return (
              <span
                key={hm.id}
                data-hosted-manager={hm.id}
                data-manager-state={attention ?? (hm.online ? "online" : "offline")}
                title={[hm.name, statusText, hm.version].filter(Boolean).join(" · ")}
                className={cx(
                  "inline-flex min-w-0 max-w-full items-center gap-1.5 rounded-full px-2 py-0.5 text-submeta font-medium",
                  warn ? "bg-orange-tint text-orange" : hm.online ? "bg-inset text-ink-2 shadow-hairline" : "bg-field text-ink-3",
                )}
              >
                <Icon name="spark" size={11} className={cx("shrink-0", warn ? "text-orange" : hm.online ? "text-ai" : "text-ink-3")} />
                <span className={cx("truncate", !warn && hm.online && "text-ink")}>{t("machines.hosted_assistant", { name: hm.name })}</span>
                <span aria-hidden className={cx("size-1.5 shrink-0 rounded-full", warn ? "bg-orange" : hm.online ? "bg-green" : "bg-ink-3")} />
                <span className={cx("shrink-0 whitespace-nowrap", !warn && hm.online && "text-green")}>{statusText}</span>
              </span>
            );
          })}
        </div>
      )}
      {pending && (
        <div className="mx-4 mt-2 flex flex-wrap items-center gap-2 rounded-[8px] bg-orange-tint px-2.5 py-2 text-[12px] text-ink" data-trust="pending">
          <Icon name="shield" size={14} className="shrink-0 text-orange" />
          <span className="min-w-0 flex-1 basis-48">{m.online ? t("machines.pending_joined", { name: m.name }) : t("machines.pending_waiting")}</span>
          <span className="flex shrink-0 gap-1.5">
            {m.online ? (
              <>
                <Button tone="secondary" disabled={answering} onClick={() => void onAnswer(m, "reject")}>
                  {t("machines.reject")}
                </Button>
                <Button tone="primary" busy={answering} onClick={() => void onAnswer(m, "confirm")}>
                  {t("machines.confirm")}
                </Button>
              </>
            ) : (
              <>
                <Button tone="secondary" disabled={answering} onClick={() => void onAnswer(m, "reject")}>
                  {t("machines.remove")}
                </Button>
                {canAdd && (
                  <Button tone="primary" onClick={() => onAddAgain({ id: m.id, name: m.name })}>
                    {t("machines.new_code")}
                  </Button>
                )}
              </>
            )}
          </span>
        </div>
      )}
      {m.online && !pending && (coded || m.error) && <p className="mx-4 mt-2 rounded-[8px] bg-red-tint px-2.5 py-1.5 text-[12px] text-red">{coded || m.error}</p>}
      {/* its manager is offline: say so, not the hub's words for an unreachable assistant */}
      {!m.online && managerOffline && (
        <p className="mx-4 mt-2 break-words rounded-[8px] bg-field px-2.5 py-1.5 text-[12px] text-ink-2" data-error-code="manager_offline" title={m.error}>
          {t("settings-managers.machine_offline", { name: managerOffline })}
        </p>
      )}
      {!m.online && coded && !expectedDisconnect && !managerOffline && (
        <p className="mx-4 mt-2 break-words rounded-[8px] bg-red-tint px-2.5 py-1.5 text-[12px] text-red" data-error-code={m.error_code} title={m.error}>
          {coded}
        </p>
      )}
      {connFailure && (
        <p className="mx-4 mt-2 break-words rounded-[8px] bg-red-tint px-2.5 py-1.5 text-[12px] text-red" data-failure={connFailure.kind}>
          <span className="font-medium">{tr("node-health.failure_title", { title: connFailure.title })}</span>
          {connFailure.next}
        </p>
      )}
      {/* an offline card has no metrics under them: the chips keep their own room above the footer */}
      <AlertChips alerts={alerts} onOpen={(kind) => onAlert(m.id, kind)} className={m.online ? undefined : "mb-3"} />
      {deployment.visible && (
        <MachineDeploy machine={m} hostedManagers={hostedManagers} className="mt-2" onDetails={() => onAlert(m.id, m.deploy_error ? "deploy_failed" : "mismatch")} onClose={NOOP} />
      )}
      {onlyComputer && !pending && m.online && <div className="flex flex-wrap gap-2 px-4 py-2">
        <Button tone="primary" className="[@media(pointer:coarse)]:min-h-11" icon="plus" onClick={() => openNewAgent({ machineId: m.id })}>{t("space-page.new_agent")}</Button>
        {(["files", "storage"] as const).map(subview => {
          const to = { ...detail, subview };
          return <a key={subview} href={href(to)} onClick={e => onLinkClick(e, to)}
            className="inline-flex h-8 items-center gap-1.5 whitespace-nowrap rounded-control bg-surface px-3 text-label font-medium text-ink shadow-btn hover:bg-hover [@media(pointer:coarse)]:min-h-11">
            <Icon name={subview === "files" ? "folder" : "archive"} size={14} stroke={2.1} />
            {t(subview === "files" ? "machines.view_files" : "machines.view_storage")}
          </a>;
        })}
      </div>}
      <MachineMetricsSummary machine={m} metrics={mm} />
      {/* the gap keeps the state and the versions apart when the versions have to shorten */}
      <div className="flex items-center justify-between gap-3 border-t border-line px-4 py-2 text-[11.5px] text-ink-3">
        <span className="whitespace-nowrap" data-agents={m.online && !pending ? known : undefined}>{pending && !m.online ? t("machines.never_connected") : !m.online ? expectedDisconnect ? t("machines.waiting_node") : t("machines.unreachable") : known === "loading" ? t("machines-machinedetail.agents_loading") : known === "failed" ? t("machines-machinedetail.agents_failed") : agents ? working ? t("machines.agents_working", { count: agents, working }) : t("machines.agents", { count: agents }) : t("machines.no_agents")}</span>
        <span className="flex min-w-0 items-center gap-3">
          <span className="truncate font-mono max-sm:hidden" title={versions || undefined}>{versions}</span>
          <a href={href(detail)} onClick={(e) => onLinkClick(e, detail)} className="shrink-0 whitespace-nowrap text-ink-2 hover:text-ink">{t("machines.details")}</a>
        </span>
      </div>
    </section>
  );
}, sameCard);

const NO_MANAGERS: readonly Manager[] = [];

// Every machines push brings new machine objects with last_seen moved on. The card reads last_seen only for whether
// there is one (neverConnected), so a machine that differs in nothing else draws the same card.
function sameCard(a: CardProps, b: CardProps): boolean {
  for (const k of Object.keys(b) as (keyof CardProps)[]) {
    if (k === "m" || k === "hostedManagers") continue;
    if (!Object.is(a[k], b[k])) return false;
  }
  if (!sameHostedManagers(a.hostedManagers, b.hostedManagers)) return false;
  return a.m === b.m || cardKey(a.m) === cardKey(b.m);
}

function sameHostedManagers(a: readonly Manager[], b: readonly Manager[]): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    const x = a[i]!, y = b[i]!;
    if (x.id !== y.id || x.name !== y.name || x.online !== y.online || x.version !== y.version || !!x.pending_restart !== !!y.pending_restart) return false;
  }
  return true;
}

const cardKey = (m: Machine) => JSON.stringify({ ...m, last_seen: !!m.last_seen });

/** Counts use the same folded session list as the Agents page. */
function agentCounts(list: readonly Session[]) {
  const by = new Map<string, { total: number; working: number; needsYou: number; waitingSid?: string }>();
  for (const s of list) {
    if (s.assistant) continue;
    const state = activityOf(s);
    if (state === "offline") continue;
    const counts = by.get(s.machine_id) ?? { total: 0, working: 0, needsYou: 0 };
    counts.total++;
    if (state === "working") counts.working++;
    if (state === "needs_you") {
      counts.needsYou++;
      counts.waitingSid ??= s.session_id;
    }
    by.set(s.machine_id, counts);
  }
  return by;
}

type MachinesView = "resources" | "cards" | "capabilities";
const VIEW_KEY = "persona.machines.view";
function fleetChoice(value: unknown): MachinesView {
  return value === "capabilities" || value === "resources" ? value : "cards";
}

function FleetScroll({ view, children }: { view: MachinesView; children: React.ReactNode }) {
  const box = useRef<HTMLDivElement>(null);
  useScrollMemory(box, "machines/" + view);
  return <div ref={box} className="min-h-0 flex-1 overflow-y-auto">{children}</div>;
}

/** The launcher health records (GET /api/v1/launchers/health), kept current by the hub's `launcher_health` events and
 *  read again every minute while the page is shown; none from a hub without them. `merge` takes records a check returned,
 *  `reload` reads them all again now. */
function useLauncherHealth(): { list: LauncherHealthRecord[]; merge: (records: LauncherHealthRecord[]) => void; reload: () => void } {
  const [list, setList] = useState<LauncherHealthRecord[]>([]);
  const loadRef = useRef<() => void>(() => {});
  const reload = useCallback(() => loadRef.current(), []);
  const merge = useCallback((records: LauncherHealthRecord[]) => {
    if (!records.length) return;
    setList((now) => {
      const key = (r: LauncherHealthRecord) => `${r.machine}\u0000${r.launcher}`;
      const next = new Map(now.map((r) => [key(r), r]));
      for (const r of records) next.set(key(r), r);
      return [...next.values()];
    });
  }, []);
  useEffect(() => {
    let dead = false;
    const load = () => {
      api<{ records?: LauncherHealthRecord[] }>("/launchers/health").then(
        (r) => !dead && setList(r.records ?? []),
        () => {}, // an older hub has none: no launcher alerts
      );
    };
    loadRef.current = load;
    const stop = visiblePoll(load, 60_000, documentPage);
    const off = onHubEvent("launcher_health", (msg) => {
      const record = (msg as { record?: LauncherHealthRecord }).record;
      if (record?.machine && record.launcher) merge([record]);
    });
    return () => {
      dead = true;
      loadRef.current = () => {};
      stop();
      off();
    };
  }, [merge]);
  return { list, merge, reload };
}

/** The alert sheet for the computer and alert asked for; closed (with its exit) when none is. */
function AlertSheetHost({ sheet, machines, metrics, sessions, known, records, launcherIssues, roster, onKind, onClose, onReconnect }: {
  sheet: { mid: string; kind: Exclude<AlertKind, "disk"> } | null;
  machines: Machine[];
  metrics: MachineMetrics[] | null;
  sessions: readonly Session[];
  known: AgentsKnown;
  records: { list: LauncherHealthRecord[]; merge: (records: LauncherHealthRecord[]) => void; reload: () => void };
  launcherIssues: (mid: string) => number;
  roster: ReturnType<typeof useRoster>;
  onKind: (mid: string, kind: AlertKind) => void;
  onClose: () => void;
  onReconnect: (id: string) => Promise<void>;
}) {
  const m = sheet ? machines.find((x) => x.id === sheet.mid) : undefined;
  const mm = m ? metrics?.find((x) => x.machine_id === m.id) : undefined;
  const agents = useMemo(() => (m ? sessions.filter((s) => s.machine_id === m.id && !s.assistant && activityOf(s) !== "offline") : []), [m, sessions]);
  const alerts = m ? machineAlerts({ machine: m, metrics: mm, launchers: launcherIssues(m.id), managerOffline: !!offlineManagerOf(roster, m) }) : [];
  return (
    <MachineAlertSheet
      machine={m}
      kind={sheet?.kind ?? "memory"}
      alerts={alerts}
      metrics={mm}
      agents={agents}
      agentsKnown={known}
      machines={machines}
      allMetrics={metrics}
      records={records.list}
      launcherIssues={launcherIssues}
      onKind={(kind) => m && onKind(m.id, kind)}
      onClose={onClose}
      onReconnect={onReconnect}
      onRecords={records.merge}
      onReloadRecords={records.reload}
    />
  );
}

// Elements that keep their own click inside a card (the links, Reconnect, Deploy …).
const INTERACTIVE = "a,button,input,select,textarea,label,summary,[role=button],[tabindex]";

// A tap anywhere else on a machine's card opens its page (#3578): on a phone the "Details" link alone is a 48 × 17 target.
// The card stays a plain section; the title link is what keyboards and screen readers use, so no control nests in another.
function openFromCard(e: React.MouseEvent<HTMLElement>, to: Route) {
  const own = e.target instanceof Element ? e.target.closest(INTERACTIVE) : null;
  if (own && e.currentTarget.contains(own)) return;
  if (e.button !== 0 || window.getSelection()?.toString()) return; // selecting the error text to copy it is not a tap
  navigate(to);
}

// Today's tokens across the computers (#4973): the total, every token per minute now, the cache hit rate, each
// computer's share in shades of the text ink (the four kinds' colors are the charts').
function TodayTokens({ machines, metrics }: { machines: Machine[]; metrics: MachineMetrics[] | null }) {
  const shares = machines
    .map((m) => ({ id: m.id, name: m.name, usage: metrics?.find((x) => x.machine_id === m.id)?.usage }))
    .filter((s): s is { id: string; name: string; usage: NonNullable<MachineMetrics["usage"]> } => !!s.usage);
  if (!shares.length) return null;
  const today = shares.reduce<UsageTokens>((a, s) => addTokens(a, s.usage.today), {});
  const all = totalTokens(today);
  const ranked = shares.filter((s) => totalTokens(s.usage.today) > 0).sort((a, b) => totalTokens(b.usage.today) - totalTokens(a.usage.today));
  const shade = (i: number) => Math.max(0.85 - i * 0.2, 0.2);
  return (
    <section className="rounded-card bg-surface px-4 py-3 shadow-card col-span-full" data-testid="machines-tokens">
      <h2 className="text-[13px] font-medium text-ink">{t("machines-usage.today_title")}</h2>
      <div className="mt-2 flex flex-wrap items-end gap-x-8 gap-y-2">
        <Stat label={t("machines-usage.total")} value={formatTokens(all)} />
        <Stat label={t("machines-usage.now")} value={t("machines-usage.per_minute_value", { n: formatTokens(shares.reduce((a, s) => a + s.usage.per_minute, 0)) })} />
        <Stat label={t("machines-usage.hit_rate_title")} value={percentText(hitRate(today))} />
        {all > 0 && (
          <div className="min-w-[220px] flex-1 pb-1">
            <div className="flex h-2 gap-0.5 overflow-hidden rounded-full" aria-hidden>
              {ranked.map((s, i) => (
                <div key={s.id} className="h-2 bg-ink" style={{ flexGrow: totalTokens(s.usage.today), opacity: shade(i) }} />
              ))}
            </div>
            <div className="mt-1.5 flex flex-wrap gap-x-3 gap-y-0.5 text-[11.5px]">
              {ranked.map((s, i) => (
                <span key={s.id} className="flex items-center gap-1 whitespace-nowrap">
                  <span aria-hidden className="inline-block size-2 rounded-full bg-ink" style={{ opacity: shade(i) }} />
                  <span className="text-ink-2">{s.name}</span>
                  <span className="text-ink-3 tabular-nums">{formatTokens(totalTokens(s.usage.today))}</span>
                </span>
              ))}
            </div>
          </div>
        )}
      </div>
    </section>
  );
}

function Stat({ label, value, sub, warn }: { label: string; value: string; sub?: string; warn?: boolean }) {
  return (
    <div className="min-w-0">
      <div className="text-[11px] text-ink-3">{label}</div>
      <div className={cx("font-mono text-[18px] leading-tight font-medium tabular-nums", warn ? "text-red" : "text-ink")}>{value}</div>
      {sub && <div className="truncate text-[10.5px] text-ink-3">{sub}</div>}
    </div>
  );
}
