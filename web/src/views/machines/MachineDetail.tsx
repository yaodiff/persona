// Machine details (/machines/<id>, docs/server/machines.md "App", iOS MachineDetailView): one page, the most used on top — how the
// computer is (online, system, which hub when there are several), its agents by herdr space (a row opens the chat),
// its values now, CPU / memory / network / load over 1, 6 or 24 hours (hover a chart to read a moment, all follow) and
// each core now (Linux), the system, the launchers. Its engine (which one the agents come from) follows the agents. A Mac the hub's extension cua is on for gets "Computer control" after that, and "Browser" (ego lite) while ego is on; then "iPhone control" while the extension iphone is on (a Mac or a Linux computer).
// A Mac whose live screen can be shown (showsScreen) gets "View screen" in the top bar, the page's one way to it: it stays in view
// while the page scrolls, and does not wait for Cua Driver, which the screen does not use. The screen is the screen-share
// module's page of its own (core/module-host.ts `standalonePages`): while that module is off there is no button.
import { MoveToManager } from "../ManagerParts";
import { hostedManagersForMachine, offlineManagerOf, useRoster } from "../../lib/managers";
import { useEffect, useMemo, useRef, useState, type ComponentProps } from "react";
import { api, ApiError, enc, errorText, isAbort } from "../../lib/api";
import { ago, byAttention, stateOf } from "../../lib/format";
import { openUsageMap } from "../../lib/usage-map-link";
import { agentName, isTraeCli } from "../../lib/agent-kind";
import {
  agentsBySpace,
  bytesAxis,
  bytesBin,
  connectionText,
  coresNow,
  countsText,
  isEmpty,
  level,
  load as loadText,
  loadAxis,
  memPercent,
  percent,
  RANGE_KEYS,
  RANGES,
  rate,
  systemFacts,
  systemLine,
  temperature,
  usedOfTotal,
  type DetailMachine,
  type DetailMetrics,
  type Level,
  type MetricRange,
} from "../../lib/machine-detail";
import { alertLabel, machineAlerts, loadDefinition, perCoreLabel } from "../../lib/machine-alerts";
import { hubLabel, routeText } from "../../lib/mesh";
import { machineDeployment } from "../../lib/machine-deploy";
import { classifyConnection } from "../../lib/node-health";
import { SCREEN_PAGE, screenPath, showsScreen } from "../../lib/computer";
import { standaloneOn, useModuleStates } from "../../core/module-host";
import { osFamily } from "../../lib/machine-capabilities";
import { MachineToolView, MachineViewLinks } from "./MachineViews";
import type { MachineSubview } from "../../lib/machine-route";
import { useScrollMemory } from "../../lib/scroll-memory";
import { href, navigate, onLinkClick, routeAt, useRoute, type Route } from "../../lib/router";
import { getState, refresh, useStore } from "../../lib/store";
import { useFullAgentSessions } from "../../lib/sessions";
import type { Launcher, MachineLinkSwitch } from "../../lib/types";
import { Button, cx, EASE, IconButton, Loading, toast } from "../../ui/atoms";
import { AgentRow } from "./AgentRow";
import { Icon } from "../../ui/icons";
import { ProviderName } from "../../ui/ProviderName";
import { MetricChart, type ChartSeries } from "./MetricChart";
import { HostedManagersList } from "./HostedManagersList";
import { lazyOverlay } from "../LazyOverlay";
import { homeUser, LinkSwitch } from "./LinkSwitch";
import { BrowserSection } from "./BrowserSection";
import { ComputerSection } from "./ComputerSection";
import { IPhoneSection } from "./IPhoneSection";
import { EngineSection } from "./EngineSection";
import { openNewAgent } from "../../lib/new-agent";
import { openMachineTerminal } from "../../lib/machine-terminal";
import { sessionWorkspace } from "../../lib/agent-repos";
import { useWorkspaceCatalog, type HostWorkspace } from "../../lib/workspace-catalog";
import { boardAgentPreset } from "../../lib/workspace-agent-preset";
import { BotmuxSwitch } from "./BotmuxSwitch";
import { ENGINE_HOSTS } from "../../lib/engines";
import { NodeHealth } from "./NodeHealth";
import { UsageSection } from "./UsageSection";
import { MachineExtensionSections } from "../extensions/ExtensionSlots";
import { t } from "../../i18n";

// MACHINE_RENAME_FEATURE: the hub renames computers (PUT /api/v1/machines/{id}/name, docs/server/machines.md
// "Renaming a computer"); an older one keeps the name its hub.json has.
const MACHINE_RENAME_FEATURE = "machine_rename";

const NOOP = () => {};
type MachineDeployProps = ComponentProps<typeof import("./MachineDeploy").MachineDeploy>;

// Loaded strictly on demand when this computer has a deployment status, error, or version mismatch (no idle preload).
const MachineDeploy = lazyOverlay<MachineDeployProps & { onClose: () => void }>(() =>
  import("./MachineDeploy").then(({ MachineDeploy: Deploy }) => function LazyMachineDeploy(props: MachineDeployProps & { onClose: () => void }) {
    return <Deploy machine={props.machine} className={props.className} onDetails={props.onDetails} hostedManagers={props.hostedManagers} />;
  }),
);

// RenameMachine edits what the hub calls the computer, in place of its title row: the hub writes hub.json and every
// app and the computer's Persona Link follow (a machines event), with no restart or reconnect. Empty is its id again.
function RenameMachine({ m, onDone }: { m: DetailMachine; onDone: () => void }) {
  const [value, setValue] = useState(m.name && m.name !== m.id ? m.name : "");
  const [busy, setBusy] = useState(false);
  const save = async (e?: React.FormEvent) => {
    e?.preventDefault();
    setBusy(true);
    try {
      const r = await api<{ name: string }>(`/machines/${enc(m.id)}/name`, { method: "PUT", json: { name: value.trim() } });
      await refresh().catch(() => {});
      toast(t("machines-machinedetail.renamed", { name: r.name }), "ok");
      onDone();
    } catch (err) {
      toast(errorText(err), "error");
      setBusy(false);
    }
  };
  return (
    <form onSubmit={save} className="flex flex-wrap items-center gap-2" onKeyDown={(e) => e.key === "Escape" && (e.stopPropagation(), onDone())}>
      <input
        autoFocus
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder={m.id}
        aria-label={t("machines-machinedetail.rename_label")}
        maxLength={40}
        className="h-8 w-[240px] max-w-full rounded-control bg-inset px-2.5 text-[13px] text-ink shadow-hairline outline-none placeholder:text-ink-3 focus:shadow-[0_0_0_1px_var(--line-strong),0_0_0_3px_var(--accent-tint)]"
      />
      <Button tone="quiet" className="h-8" onClick={onDone}>
        {t("common.cancel")}
      </Button>
      <Button tone="primary" type="submit" className="h-8" busy={busy}>
        {t("common.save")}
      </Button>
      <p className="w-full text-[11.5px] text-ink-3">{t("machines-machinedetail.rename_hint")}</p>
    </form>
  );
}

type MachineLauncher = Pick<Launcher, "name" | "display_name" | "model" | "agent" | "agent_type">;

export function MachineDetail({ mid, subview = "work", mode, onToggleSidebar }: {
  mid: string; subview?: MachineSubview; mode?: "reclaim"; onToggleSidebar: () => void;
}) {
  const address = useRoute();
  const requested = address.name === "machine" ? address.range as MetricRange : undefined;
  const range = requested && RANGE_KEYS.includes(requested) ? requested : "1h";
  const work = subview === "work";
  const management = subview === "management" || subview === "config";
  const machines = useStore((s) => s.machines) as DetailMachine[];
  const loaded = useStore((s) => s.loaded);
  useModuleStates(); // "View screen" follows the screen page's module
  // Its agents, read as the Agents list reads them: a hub that pages that list pushes no sessions, so the store has none
  // of this computer's. A child session folds into its parent (#5120): never an agent of the computer of its own.
  const full = useFullAgentSessions(work);
  const sessions = useMemo(() => full.sessions.filter((s) => s.machine_id === mid), [full.sessions, mid]);
  const m = machines.find((x) => x.id === mid);
  const online = m?.online ?? false;
  const deployment = m ? machineDeployment(m) : null;
  const expectedDisconnect = deployment?.expectedDisconnect ?? false;

  const [data, setData] = useState<{ metrics: DetailMetrics; range: MetricRange; at: number } | null>(null);
  const [failure, setFailure] = useState("");
  const [launchers, setLaunchers] = useState<MachineLauncher[] | null>(null);
  const [launcherError, setLauncherError] = useState("");
  const [reconnecting, setReconnecting] = useState(false);
  const alive = useRef(true);
  useEffect(() => () => void (alive.current = false), []);

  const setRange = (r: MetricRange) => {
    navigate({ name: "machine", mid, subview: "monitoring", range: r === "1h" ? undefined : r }, true);
  };

  // Metrics: now, then every 10 s (the hour) or every minute, and again when the page comes back into view; nothing
  // while the tab is hidden.
  const readMetrics = !!m && (work || subview === "monitoring");
  useEffect(() => {
    if (!readMetrics) return;
    let dead = false;
    let ctrl: AbortController | null = null;
    const load = async () => {
      if (document.visibilityState !== "visible") return;
      ctrl?.abort();
      ctrl = new AbortController();
      try {
        const r = await api<{ metrics: DetailMetrics }>(`/machines/${enc(mid)}/metrics?range=${range}`, { signal: ctrl.signal });
        if (dead) return;
        setData({ metrics: { ...r.metrics, points: r.metrics.points ?? [] }, range, at: Date.now() / 1000 });
        setFailure("");
      } catch (e) {
        if (dead || isAbort(e)) return;
        setFailure(e instanceof ApiError && e.status === 404 ? t("machines-machinedetail.no_metrics_hub") : errorText(e));
      }
    };
    void load();
    const timer = setInterval(load, RANGES[range].poll);
    const vis = () => void load();
    document.addEventListener("visibilitychange", vis);
    return () => {
      dead = true;
      ctrl?.abort();
      clearInterval(timer);
      document.removeEventListener("visibilitychange", vis);
    };
  }, [mid, range, readMetrics]);

  // Launchers: an offline computer has none to list; again once it comes online.
  useEffect(() => {
    if (!online || !management) {
      setLaunchers(null);
      return;
    }
    let dead = false;
    api<{ launchers: MachineLauncher[] }>(`/machines/${enc(mid)}/launchers`)
      .then((r) => !dead && (setLaunchers(r.launchers ?? []), setLauncherError("")))
      .catch((e) => !dead && setLauncherError(errorText(e)));
    return () => {
      dead = true;
    };
  }, [mid, online, management]);

  // Asks the hub to reconnect, then watches the machine list (up to 15 s) for it to come online.
  const reconnect = async () => {
    setReconnecting(true);
    try {
      await api(`/machines/${enc(mid)}/reconnect`, { method: "POST" });
    } catch (e) {
      toast(t("machines.reconnect_failed", { error: errorText(e) }), "error");
      setReconnecting(false);
      return;
    }
    for (let i = 0; i < 10 && alive.current; i++) {
      await new Promise((r) => setTimeout(r, 1500));
      await refresh().catch(() => {});
      if (getState().machines.find((x) => x.id === mid)?.online) {
        if (alive.current) setReconnecting(false);
        toast(t("machines-machinedetail.connected"), "ok");
        return;
      }
    }
    if (!alive.current) return;
    setReconnecting(false);
    const err = getState().machines.find((x) => x.id === mid)?.error;
    toast(err ? t("machines-machinedetail.not_connected_error", { error: err }) : t("machines-machinedetail.not_connected"), "error");
  };

  // its manager offline (several managers): that is why, and Reconnect would go through it
  const roster = useRoster();
  const goneWith = m ? offlineManagerOf(roster, m) : null;
  const hostedManagers = useMemo(() => (m ? hostedManagersForMachine(roster, m) : []), [roster, m]);
  // a linked hub's computer is that hub's to give (`not_movable`): in a mesh every computer names its hub
  const selfHub = useStore((s) => s.mesh.find((x) => x.self)?.hub_id);
  const ownHub = !m?.hub_id || !selfHub || m.hub_id === selfHub; // one of this hub's own computers
  const connFailure = m && !expectedDisconnect && !goneWith ? classifyConnection(m) : null;
  const panesOn = useStore(s => s.features.includes("terminal_panes"));
  const catalog = useWorkspaceCatalog(Boolean(work && m && ownHub));
  const inventory = useMemo(() => ownHub ? catalog.workspaces.filter(w => w.machine_id === mid && w.source !== "session-directory") : [], [catalog.workspaces, mid, ownHub]);
  const groups = useMemo(() => {
    if (!online) return [];
    const groups = agentsBySpace(sessions, s => sessionWorkspace(s, inventory, catalog.manager));
    for (const g of groups) g.sessions.sort(byAttention);
    return groups.sort((a, b) => Number(b.counts.needs > 0) - Number(a.counts.needs > 0) || Number(b.counts.working > 0) - Number(a.counts.working > 0));
  }, [online, sessions, inventory, catalog.manager]);
  const waiting = groups.flatMap(g => g.sessions.filter(s => stateOf(s) === "needs_you"));
  const displayed = new Set(groups.map(g => g.workspace));
  const otherWorkspaces = inventory.filter(w => !displayed.has(w) && !w.hidden_by_directory);
  const discoveryFailed = catalog.failedMachines === null || catalog.failedMachines?.includes(mid);
  const disk = m && machineAlerts({ machine: m, metrics: data && data.metrics.latest ? { ...data.metrics, latest: data.metrics.latest } : undefined, managerOffline: Boolean(goneWith) }).find(a => a.kind === "disk");
  const agentCount = groups.reduce((n, g) => n + g.sessions.length, 0);
  const hub = hubLabel(m, machines);
  const route = routeText(m, machines);
  const system = data?.metrics.system ?? m?.system;
  // a hub that renames computers, and one of its own (a linked hub's is renamed there)
  const canRename = useStore((s) => s.features.includes(MACHINE_RENAME_FEATURE)) && m?.kind !== "hub" && ownHub;
  const [renaming, setRenaming] = useState(false);

  if (!m) {
    return (
      <Page mid={mid} subview={subview} title={t("machines.computer_label")} onToggleSidebar={onToggleSidebar}>
        {!loaded ? (
          <Loading />
        ) : (
          <div className="flex flex-col items-start gap-3 rounded-card bg-surface p-4 shadow-card">
            <p className="text-[13.5px] font-medium text-ink">{t("machines-machinedetail.not_found")}</p>
            <p className="text-[12.5px] text-ink-2">{t("machines-machinedetail.not_found_detail", { id: mid })}</p>
            <a href={href({ name: "machines" })} onClick={(e) => onLinkClick(e, { name: "machines" })} className="text-[12.5px] font-medium text-accent-ink hover:underline">
              {t("machines-machinedetail.back_to_list")}
            </a>
          </div>
        )}
      </Page>
    );
  }

  return (
    <Page
      mid={mid}
      title={m.name}
      subview={subview}
      onToggleSidebar={onToggleSidebar}
      right={
        <>
          {showsScreen(m) && standaloneOn(SCREEN_PAGE) && (
            <Button icon="monitor" href={screenPath(mid)} title={t("machines-machinedetail.screen_hint")}>
              {t("machines-machinedetail.screen")}
            </Button>
          )}
        </>
      }
    >
      {/* the computer */}
      <section className="overflow-hidden rounded-card bg-surface shadow-card" style={{ animation: `fade-up 420ms ${EASE} both` }}>
        <div className="flex flex-wrap items-center gap-3 px-4 py-3.5">
          <div className="flex min-w-0 flex-1 basis-60 items-start gap-3">
            <span className={cx("flex size-10 shrink-0 items-center justify-center rounded-[10px]", online ? "bg-field text-ink-2" : "bg-inset text-ink-3")}>
              <Icon name="machine" size={18} />
            </span>
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <h2 className="truncate text-[15px] font-semibold text-ink">{m.name}</h2>
                <span className={cx("inline-flex h-5 items-center gap-1 whitespace-nowrap rounded-full px-1.5 text-[11px] font-medium", online ? "bg-green-tint text-green" : "bg-field text-ink-3")}>
                  <span className={cx("size-1.5 rounded-full", online ? "bg-green" : "bg-ink-3")} />
                  {online ? t("machines-machinedetail.online") : t("machines-machinedetail.offline")}
                </span>
                {hub && (
                  <span className="inline-flex h-5 items-center gap-1 whitespace-nowrap rounded-full bg-inset px-1.5 text-[11px] font-medium text-ink-2 shadow-hairline" title={t("machines-machinedetail.belongs_to_hub", { hub })}>
                    <Icon name="hub" size={11} />
                    {hub}
                  </span>
                )}
              </div>
              <p className="mt-0.5 truncate text-[12px] text-ink-3" title={systemLine(system, m)}>
                {systemLine(system, m)}
              </p>
              {route && <p className="mt-0.5 truncate text-[11.5px] text-ink-3" title={t("machines-machinedetail.route_hint")}>{t("machines-machinedetail.route", { route })}</p>}
            </div>
          </div>
          {online && <div className="flex flex-wrap items-center gap-2">
            <Button tone="primary" className="[@media(pointer:coarse)]:min-h-11" icon="plus" onClick={() => openNewAgent({ machineId: mid })}>{t("space-page.new_agent")}</Button>
            {panesOn && <Button className="[@media(pointer:coarse)]:min-h-11" icon="terminal" onClick={() => openMachineTerminal({ mid })}>{t("sidebar.nav_terminal")}</Button>}
          </div>}
          {!online && !expectedDisconnect && !goneWith && (
            <Button tone="secondary" icon="retry" busy={reconnecting} onClick={() => void reconnect()}>
              {reconnecting ? t("machines-machinedetail.connecting") : t("machines.reconnect")}
            </Button>
          )}
        </div>
        {!online && !expectedDisconnect && (
          <div className="mx-4 mb-3.5 rounded-[8px] bg-red-tint px-2.5 py-1.5 text-[12px] text-red" data-offline-box>
            {/* The plain reason and next step lead when the error text says why; the raw error then goes last, small and
                grey, for whoever debugs it. Without a known reason the raw text (or the generic line) stays first. */}
            {goneWith ? (
              <p className="break-words font-medium" data-error-code="manager_offline" title={m.error}>{t("settings-managers.machine_offline", { name: goneWith.name })}</p>
            ) : connFailure ? (
              <p className="break-words font-medium" data-failure={connFailure.kind}>{t("node-health.failure_line", { title: connFailure.title, next: connFailure.next })}</p>
            ) : (
              <p className="break-words">{m.error || t("machines-machinedetail.unreachable")}</p>
            )}
            <p className="mt-0.5 text-red/80">{[m.last_seen && ago(m.last_seen) && t("machines-machinedetail.last_seen", { when: ago(m.last_seen) }), connectionText(m)].filter(Boolean).join(" · ")}</p>
            {connFailure && m.error && <p className="mt-1 break-words font-mono text-[11px] text-ink-3" data-raw-error>{m.error}</p>}
          </div>
        )}
        {online && m.error && <p className="mx-4 mb-3.5 rounded-[8px] bg-red-tint px-2.5 py-1.5 text-[12px] text-red">{m.error}</p>}
      </section>
      <MachineViewLinks mid={mid} view={subview} />
      {deployment?.visible && !management && <div role="status" className="flex flex-wrap items-center gap-2 rounded-card bg-surface px-4 py-2 text-label text-orange shadow-card">
        <span>{deployment.message} {deployment.error}</span>
        <a className="text-accent-ink" href={href({ name: "machine", mid, subview: "management" })}
          onClick={e => onLinkClick(e, { name: "machine", mid, subview: "management" })}>{t("machines.view_management")}</a>
      </div>}
      {work && disk && <div className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-card bg-surface shadow-card px-4 py-1">
        <span className="inline-flex items-center gap-2 text-sm font-medium text-ink"><Icon name="archive" size={14} className="text-orange" />{alertLabel(disk)}</span>
        <Button tone="quiet" className="min-h-11" onClick={() => openUsageMap(mid)}>{t("machines-usagemap.reclaim_open")}</Button>
      </div>}

      {work && <>
        {waiting.length > 0 && <section>
          <Heading title={t("lib-state-labels.needs_you")} count={waiting.length} />
          <div className="divide-y divide-line overflow-hidden rounded-card bg-surface shadow-card">
            {waiting.map(s => <AgentRow key={s.session_id} s={s} />)}
          </div>
        </section>}

        {/* agents */}
        <section>
          {/* until its agents are read, how many there are is unknown: no count, and no "No agents running" */}
          <Heading title="Agent" count={online && full.loaded ? agentCount : undefined} />
          {groups.length === 0 ? (
            <p className="rounded-card bg-surface px-4 py-3 text-[12.5px] text-ink-3 shadow-card" data-agents={online ? (full.loaded ? "yes" : full.error ? "failed" : "loading") : undefined}>
              {!online ? t("machines-machinedetail.agents_offline") : full.loaded ? t("machines-machinedetail.no_agents") : full.error ? t("machines-machinedetail.agents_failed_detail") : t("machines-machinedetail.agents_loading")}
            </p>
          ) : (
            <div className="flex flex-col gap-2.5">
              {groups.map((g, gi) => (
                <div key={g.key} className="overflow-hidden rounded-card bg-surface shadow-card" style={{ animation: `fade-up 420ms ${EASE} ${Math.min(gi, 6) * 50}ms both` }}>
                  <div className="flex items-center gap-2 border-b border-line px-4 py-2">
                    <Icon name={g.key ? "layers" : "agent"} size={13} className="shrink-0 text-ink-3" />
                    <div className="min-w-0 flex-1">
                    {g.key ? (
                      // a space's name opens its page (/space/…); agents outside any space have none
                      <a
                        href={href({ name: "space", mid, space: g.name, workspace: g.workspace?.id })}
                        onClick={(e) => onLinkClick(e, { name: "space", mid, space: g.name, workspace: g.workspace?.id })}
                        className="group/space flex min-w-0 items-center gap-0.5 truncate rounded-[5px] text-[12.5px] font-medium text-ink hover:text-accent-ink"
                        title={t("machines-machinedetail.open_space", { name: g.name })}
                      >
                        <span className="truncate">{g.name}</span>
                        <Icon name="chevronRight" size={11} stroke={2.4} className="shrink-0 text-ink-3 transition-transform duration-150 group-hover/space:translate-x-0.5" />
                      </a>
                    ) : (
                      <span className="truncate text-[12.5px] font-medium text-ink">{g.name}</span>
                    )}
                    <span className="block truncate text-submeta text-ink-3">{countsText(g.counts)}</span>
                    </div>
                    {g.key && <WorkspaceActions mid={mid} cwd={g.workspace?.cwd || g.sessions[0]?.workspace_cwd || g.sessions[0]?.cwd}
                      workspace={g.workspace} panesOn={panesOn} online={online} />}
                  </div>
                  <div className="divide-y divide-line">
                    {g.sessions.filter(s => stateOf(s) !== "needs_you").map((s) => (
                      <AgentRow key={s.session_id} s={s} />
                    ))}
                  </div>
                </div>
              ))}
            </div>
          )}
        </section>

        {ownHub && <section>
          <Heading title={t("machines-machinedetail.other_workspaces")} />
          <div className="divide-y divide-line rounded-card bg-surface shadow-card">
            {otherWorkspaces.map(w => {
              const name = w.title || w.branch || w.cwd;
              const to = { name: "space" as const, mid, space: name, workspace: w.id };
              return <div key={w.id} className="flex items-center gap-2 px-4 py-1">
                <Icon name="layers" size={14} className="shrink-0 text-ink-3" />
                <a href={href(to)} onClick={e => onLinkClick(e, to)}
                  className="flex min-h-11 min-w-0 flex-1 items-center text-sm font-medium text-ink hover:text-accent-ink"><span className="truncate">{name}</span></a>
                <WorkspaceActions mid={mid} cwd={w.cwd} workspace={w} panesOn={panesOn} online={online} />
              </div>;
            })}
            {(!online || discoveryFailed || catalog.failedMachines === undefined || !otherWorkspaces.length) &&
              <p className="px-4 py-3 text-sm text-ink-3">{!online ? t("space-page.machine_offline") : discoveryFailed ? t("machines-machinedetail.workspaces_failed")
                : catalog.failedMachines === undefined ? t("machines-machinedetail.workspaces_loading") : t("machines-machinedetail.no_other_workspaces")}</p>}
          </div>
        </section>}
      </>}
      {subview === "monitoring" && <>
        {/* its token usage, then how busy it is, then the engine and what it can do (#4973, as in the apps) */}
        <UsageSection mid={mid} heading={(right) => <Heading title={t("machines-usage.title")} right={right} />} />

        {/* monitoring */}
        <section>
          <Heading
            title={t("machines-machinedetail.monitoring")}
            right={
              <div className="flex items-center gap-0.5 rounded-control bg-inset p-0.5 shadow-hairline" role="radiogroup" aria-label={t("machines-machinedetail.range")}>
                {RANGE_KEYS.map((k) => (
                  <button
                    key={k}
                    type="button"
                    role="radio"
                    aria-checked={range === k}
                    onClick={() => setRange(k)}
                    className={cx(
                      "flex h-7 items-center whitespace-nowrap rounded-[7px] px-2.5 text-[12px] transition-[background-color,color] duration-150",
                      range === k ? "bg-surface font-medium text-ink shadow-btn" : "text-ink-3 hover:text-ink-2",
                    )}
                  >
                    {RANGES[k].label}
                  </button>
                ))}
              </div>
            }
          />
          {!data ? (
            failure ? (
              <div className="rounded-card bg-surface px-4 py-6 text-center shadow-card">
                <p className="text-[13px] font-medium text-ink">{t("machines-machinedetail.no_metrics")}</p>
                <p className="mt-1 text-[12px] text-ink-3">{failure}</p>
              </div>
            ) : (
              <div className="flex h-40 items-center justify-center rounded-card bg-surface shadow-card">
                <Loading />
              </div>
            )
          ) : (
            <Monitor data={data} range={range} online={online} system={system} onRange={setRange} machine={{ id: m.id, name: m.name }} />
          )}
          <p className="mt-2 px-1 text-submeta text-ink-3">{range === "1h" ? t("machines-machinedetail.refresh_10s") : t("machines-machinedetail.refresh_1m")}</p>
          {data && failure && <p className="mt-2 px-1 text-[11.5px] text-red">{t("machines-machinedetail.refresh_failed", { error: failure })}</p>}
          <p className="mt-2 px-1 text-[11.5px] leading-relaxed text-ink-3">{t("machines-machinedetail.metrics_note")}</p>
          {/* what load counts, under the tile and the chart that show it (#12519) */}
          {data && <p className="mt-1 px-1 text-submeta leading-relaxed text-ink-3" data-testid="load-definition">{loadDefinition()}</p>}
        </section>
      </>}
      {subview === "diagnostics" && <>
        {/* what this computer can do for Persona, from the node's self-check (docs/server/machines.md "Doctor: what this computer can do") */}
        <NodeHealth machine={m} heading={null} />

        {/* system */}
        <section>
          <Heading title={t("machines-machinedetail.system")} />
          <div className="overflow-hidden rounded-card bg-surface shadow-card">
            <dl className="-mb-px grid grid-cols-1 md:grid-cols-2">
              {systemFacts(system, m, Date.now() / 1000).map(([k, v, mono]) => (
                <div key={k} className="flex items-baseline gap-3 border-b border-line px-4 py-2 md:odd:border-r">
                  <dt className="w-[84px] shrink-0 whitespace-nowrap text-[12px] text-ink-3">{k}</dt>
                  <dd className={cx("min-w-0 flex-1 text-[12.5px] break-words text-ink-2 select-text", mono && "font-mono text-[12px]")}>{v}</dd>
                </div>
              ))}
            </dl>
          </div>
        </section>
      </>}
      {management && <>
        <section>
          <Heading title={t("machines-machinedetail.configuration")} />
          <EngineSection key={m.id} machine={m} heading={<h3 className="mb-2 flex items-center gap-2 text-sm font-medium text-ink"><Icon name="gear" size={14} />{t("machines-machinedetail.engine")}</h3>} />
          <div className="my-3 flex flex-wrap items-center gap-2">
            {canRename && (renaming ? <RenameMachine m={m} onDone={() => setRenaming(false)} /> :
              <Button icon="write" onClick={() => setRenaming(true)}>{t("machines-machinedetail.rename")}</Button>)}
            <MoveToManager machineId={m.id} manager={m.manager} movable={m.kind !== "hub" && ownHub} />
          </div>
          {deployment?.visible && <MachineDeploy machine={m} hostedManagers={hostedManagers} className="mb-3.5" onClose={NOOP} />}
          <div className="my-3"><BotmuxSwitch key={m.id} machineID={m.id} /></div>
          {/* switching it to Persona Link (#10662), on this hub's own computer (a linked hub's switches there); while one
              runs, its disconnect is expected (machineDeployment) */}
          <LinkSwitch
            machineId={m.id}
            name={m.name}
            state={ownHub ? m.link_switch : undefined}
            post={() => api<{ link_switch?: MachineLinkSwitch }>(`/machines/${enc(m.id)}/link-switch`, { method: "POST" })}
            refresh={refresh}
            user={homeUser(m.home)}
            className="mx-4 mb-3.5"
          />
          <HostedManagersList managers={hostedManagers} nodeVersion={m.node_version} className="border-t border-line px-4 py-3" />
        </section>

        {m.computer && <ComputerSection mid={mid} computer={m.computer} online={online} manager={m.manager} />}
        {m.browser && <BrowserSection mid={mid} browser={m.browser} online={online} />}
        {m.iphone && <IPhoneSection mid={mid} iphone={m.iphone} online={online} mac={osFamily(m) === "macos"} />}

        {/* launchers */}
        <section>
          <Heading title={t("machines-machinedetail.launchers")} count={online && launchers?.length ? launchers.length : undefined} />
          <div className="overflow-hidden rounded-card bg-surface shadow-card">
            {!online ? (
              <p className="px-4 py-3 text-[12.5px] text-ink-3">{t("machines-machinedetail.launchers_offline")}</p>
            ) : launcherError && !launchers ? (
              <p className="px-4 py-3 text-[12.5px] text-red">{t("machines-machinedetail.launchers_failed", { error: launcherError })}</p>
            ) : !launchers ? (
              <div className="px-4 py-3">
                <Loading />
              </div>
            ) : launchers.length === 0 ? (
              <p className="px-4 py-3 text-[12.5px] text-ink-3">{t("machines-machinedetail.no_launchers")}</p>
            ) : (
              <div className="divide-y divide-line">
                {launchers.map((l) => {
                  const kind = l.agent_type || l.agent || "claude";
                  return (
                    <div key={l.name} className="flex items-center gap-3 px-4 py-2">
                      {/* a provider's display name, its startup ID beside it (#5029); an rc alias's ID in mono, as before */}
                      <ProviderName of={l} className="min-w-0 truncate text-[12.5px] text-ink" idClassName="min-w-0 shrink-[2] truncate text-[11.5px] text-ink-3" />
                      {kind !== "claude" && <span className="inline-flex h-5 shrink-0 items-center whitespace-nowrap rounded-full bg-inset px-1.5 text-[11px] font-medium text-ink-2 shadow-hairline">{kind === "codex" || isTraeCli(kind) ? agentName(kind) : kind}</span>}
                      <span className="flex-1" />
                      {l.model && <span className="shrink-0 truncate font-mono text-[11.5px] text-ink-3">{l.model}</span>}
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        </section>

        {/* the extensions' sections (docs/extensions/extensions.md "UI slots" machine_section), after the machine's own */}
        <MachineExtensionSections mid={mid} heading={(title, right) => <Heading title={title} right={right} />} />
      </>}
      <MachineToolView machine={m} view={subview} mode={mode} path={address.name === "machine" && address.mid === mid ? address.path : undefined} />
    </Page>
  );
}

function WorkspaceActions({ mid, cwd, workspace, panesOn, online }: {
  mid: string; cwd?: string; workspace?: HostWorkspace; panesOn: boolean; online: boolean;
}) {
  if (!cwd) return null;
  const disabled = !online || workspace?.available === false || workspace?.online === false;
  const binding = workspace && ENGINE_HOSTS.includes(workspace.source) ? { host: workspace.source, workspaceId: workspace.source_id } : {};
  return <div className="flex shrink-0 items-center">
    <IconButton icon="plus" size={44} label={t("space-page.new_agent")} disabled={disabled}
      onClick={() => openNewAgent(workspace ? boardAgentPreset(workspace) : { machineId: mid, cwd })} />
    <IconButton icon="folder" size={44} label={t("machines-machinedetail.files")} disabled={disabled} onClick={() => navigate({ name: "machine", mid, subview: "files", path: cwd })} />
    {panesOn && <IconButton icon="terminal" size={44} label={t("sidebar.nav_terminal")} disabled={disabled} onClick={() => openMachineTerminal({ mid, cwd, ...binding })} />}
  </div>;
}

function Page({ mid, subview, title, right, onToggleSidebar, children }: { mid: string; subview: MachineSubview; title: string; right?: React.ReactNode; onToggleSidebar: () => void; children: React.ReactNode }) {
  const box = useRef<HTMLDivElement>(null);
  useScrollMemory(box, href({ name: "machine", mid, subview }));
  const previous = history.state?.machineBack;
  const parent: Route = subview === "work" ? { name: "machines" } : { name: "machine", mid };
  const back = typeof previous === "string" && previous.startsWith("/") && routeAt(previous) ? previous : undefined;
  return (
    <div className="flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
        <IconButton icon="sidebar" label={t("machines-machinedetail.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
        <a
          href={back ?? href(parent)}
          onClick={e => {
            if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
            e.preventDefault();
            if (back) history.back();
            else navigate(parent, true);
          }}
          className="flex min-h-11 shrink-0 items-center gap-0.5 whitespace-nowrap rounded-[7px] pr-2 pl-1 text-[12.5px] text-ink-3 transition-colors hover:bg-hover hover:text-ink"
        >
          <Icon name="chevronLeft" size={14} stroke={2.2} />
          {t(back || parent.name === "machine" ? "machines.view_back" : "machines-machinedetail.machines")}
        </a>
        <h1 className="min-w-0 flex-1 truncate text-[14px] font-semibold text-ink" title={mid}>
          {title}
        </h1>
        {right}
      </header>
      <div ref={box} className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex max-w-[880px] flex-col gap-4 px-4 py-4 sm:px-8">{children}</div>
      </div>
    </div>
  );
}

function Heading({ title, count, right }: { title: string; count?: number; right?: React.ReactNode }) {
  return (
    <div className="mb-2.5 flex min-h-7 items-center gap-2 px-1">
      <h2 className="text-[13px] font-medium text-ink">{title}</h2>
      {count !== undefined && <span className="text-[12px] text-ink-3 tabular-nums">{count}</span>}
      <span className="flex-1" />
      {right}
    </div>
  );
}

// ── monitoring: the values now, then the charts ──

function Monitor({
  data,
  range,
  online,
  system,
  onRange,
  machine,
}: {
  data: { metrics: DetailMetrics; range: MetricRange; at: number };
  range: MetricRange;
  online: boolean;
  system: DetailMachine["system"];
  onRange: (r: MetricRange) => void;
  /** The disk tile opens its disk map (#14576). */
  machine?: { id: string; name: string };
}) {
  const { metrics } = data;
  const live = online ? (metrics.latest ?? undefined) : undefined;
  const perCore = coresNow(metrics.latest, online);

  // Nothing in the whole range (offline all along, or just started): one line instead of three empty charts.
  if (isEmpty(metrics.points))
    return (
      <>
        {live && <NowTiles s={live} cores={system?.cores} machine={machine} />}
        {coresNow(metrics.latest, online) && <CoreBars cores={coresNow(metrics.latest, online)!} />}
        <div className={cx("flex flex-col items-center gap-2.5 rounded-card bg-surface px-4 py-7 text-center shadow-card transition-opacity duration-200", data.range !== range && "opacity-60")}>
          <p className="text-[12.5px] text-ink-2">
            {online ? t("machines-machinedetail.just_started") : t("machines-machinedetail.offline_range", { range: RANGES[data.range].label })}
          </p>
          {!online && data.range !== "24h" && (
            <Button tone="secondary" onClick={() => onRange("24h")}>
              {t("machines-machinedetail.show_24h")}
            </Button>
          )}
        </div>
      </>
    );

  return (
    <>
      {live && <NowTiles s={live} cores={system?.cores} machine={machine} />}
      <Charts data={data} range={range} live={live} system={system} />
      {perCore && <CoreBars cores={perCore} />}
    </>
  );
}

const percentAxis = (v: number) => `${v}%`;

/** The four charts and the moment they share. The moment lives here, not in the page: a pointer move re-renders these
 * charts (each only its crosshair and read-out), not the tiles, the cores, the agents or the launchers. */
function Charts({
  data,
  range,
  live,
  system,
}: {
  data: { metrics: DetailMetrics; range: MetricRange; at: number };
  range: MetricRange;
  live: NonNullable<DetailMetrics["latest"]> | undefined;
  system: DetailMachine["system"];
}) {
  const { metrics } = data;
  const r = RANGES[data.range];
  const t1 = data.at,
    t0 = t1 - r.secs;
  const memTotal = live?.mem_total ?? system?.memory ?? 0;
  const memPct = memPercent(live?.mem_used, live?.mem_total);

  const cpu = useMemo<ChartSeries[]>(() => [{ name: "CPU", color: "var(--accent)", read: (p) => p.cpu, now: live?.cpu }], [live?.cpu]);
  const mem = useMemo<ChartSeries[]>(() => [{ name: t("machines-machinedetail.memory"), color: "var(--green)", read: (p) => (p.mem !== undefined && memTotal ? (100 * p.mem) / memTotal : undefined), now: memPct }], [memTotal, memPct]);
  const net = useMemo<ChartSeries[]>(
    () => [
      { name: t("machines-machinedetail.download"), color: "var(--orange)", read: (p) => p.rx, now: live?.net_rx },
      { name: t("machines-machinedetail.upload"), color: "var(--accent)", read: (p) => p.tx, now: live?.net_tx },
    ],
    [live?.net_rx, live?.net_tx],
  );
  const load = useMemo<ChartSeries[]>(() => [{ name: t("machines-machinedetail.load"), color: "var(--chart-load)", read: (p) => p.load, now: live?.load }], [live?.load]);
  const cores = system?.cores ?? 0;

  // Picking another range lets go of the moment (React's "adjusting some state when a prop changes").
  const [hover, setHover] = useState<number | null>(null);
  const [hoverRange, setHoverRange] = useState(range);
  if (hoverRange !== range) {
    setHoverRange(range);
    setHover(null);
  }
  const common = { points: metrics.points, t0, t1, step: r.step, hover, onHover: setHover };

  return (
    <div className={cx("grid grid-cols-1 gap-3 transition-opacity duration-200 md:grid-cols-2", data.range !== range && "opacity-60")}>
      <MetricChart title="CPU" series={cpu} percent format={percent} axisFormat={percentAxis} {...common} />
      <MetricChart title={memTotal ? t("machines-machinedetail.memory_total", { total: bytesBin(memTotal).replace(/\.00 /, " ") }) : t("machines-machinedetail.memory")} series={mem} percent format={percent} axisFormat={percentAxis} {...common} />
      <MetricChart title={t("machines-machinedetail.network")} series={net} binary format={rate} axisFormat={bytesAxis} {...common} />
      <MetricChart title={t("machines-machinedetail.load_1m")} series={load} format={loadText} axisFormat={loadAxis} reference={cores > 0 ? { value: cores, label: t("machines-machinedetail.cores", { count: cores }) } : undefined} {...common} />
    </div>
  );
}

function NowTiles({ s, cores, machine }: { s: NonNullable<DetailMetrics["latest"]>; cores?: number; machine?: { id: string; name: string } }) {
  const memPct = memPercent(s.mem_used, s.mem_total);
  const diskPct = memPercent(s.disk_used, s.disk_total);
  const swapPct = s.swap_total ? memPercent(s.swap_used, s.swap_total) : undefined;
  const perCore = s.load !== undefined && cores ? s.load / cores : undefined;
  const tiles: { label: string; value: string; sub?: string; frac?: number; lv: Level; open?: () => void; openLabel?: string }[] = [
    { label: "CPU", value: percent(s.cpu), sub: cores ? t("machines-machinedetail.cores", { count: cores }) : undefined, frac: s.cpu, lv: level(s.cpu, 75, 90) },
    { label: t("machines-machinedetail.memory"), value: percent(memPct), sub: s.mem_used !== undefined ? usedOfTotal(s.mem_used, s.mem_total) : undefined, frac: memPct, lv: level(memPct, 80, 90) },
    { label: t("machines-machinedetail.network"), value: `↓ ${rate(s.net_rx)}`, sub: `↑ ${rate(s.net_tx)}`, lv: "normal" },
    // the raw load average with load per core under it, the card's and the chip's figure (#12519); the cores are under CPU
    { label: t("machines-machinedetail.load"), value: loadText(s.load), sub: perCore !== undefined ? perCoreLabel(perCore) : undefined, lv: level(perCore, 1, 2) },
    { label: t("machines-machinedetail.disk"), value: percent(diskPct), sub: s.disk_used !== undefined ? usedOfTotal(s.disk_used, s.disk_total) : undefined, frac: diskPct, lv: level(diskPct, 80, 90), open: machine && (() => openUsageMap(machine.id)), openLabel: machine && t("machines-usagemap.reclaim_open_tile", { machine: machine.name }) },
    { label: t("machines-machinedetail.swap"), value: s.swap_total ? bytesBin(s.swap_used) : t("machines-machinedetail.swap_off"), sub: s.swap_total ? t("machines-machinedetail.swap_total", { total: bytesBin(s.swap_total) }) : undefined, frac: swapPct, lv: "normal" },
  ];
  if (s.battery_percent !== undefined && Number.isFinite(s.battery_percent)) {
    const charging = s.battery_charging === true;
    tiles.push({
      label: t("machines-machinedetail.battery"),
      value: percent(s.battery_percent),
      sub: charging ? t("machines-machinedetail.charging") : undefined,
      frac: s.battery_percent,
      lv: s.battery_percent <= 15 && !charging ? "critical" : s.battery_percent <= 25 && !charging ? "warning" : "normal",
    });
  }
  if (s.temp_c !== undefined && Number.isFinite(s.temp_c)) {
    tiles.push({
      label: t("machines-machinedetail.temperature"),
      value: temperature(s.temp_c),
      frac: Math.min(100, (s.temp_c / 125) * 100),
      lv: level(s.temp_c, 75, 90),
    });
  }
  return (
    <div className="mb-3 grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-6">
      {tiles.map((tile) => {
        const Tile = tile.open ? "button" : "div";
        return (
        <Tile
          key={tile.label}
          {...(tile.open ? { type: "button" as const, onClick: tile.open, "aria-label": `${tile.label} ${tile.value}, ${tile.openLabel ?? ""}`, title: tile.openLabel, "data-testid": "disk-tile" } : {})}
          className={cx("min-w-0 rounded-card bg-surface px-2.5 py-2.5 text-left shadow-card sm:px-3", tile.open && "cursor-pointer transition-colors hover:bg-hover")}
        >
          <div className="flex items-center gap-1.5 text-[11px] text-ink-3">
            <span className="truncate">{tile.label}</span>
            {tile.lv !== "normal" && (
              <span className="inline-flex shrink-0 items-center gap-1 whitespace-nowrap text-ink-2">
                <span className={cx("size-1.5 rounded-full", tile.lv === "critical" ? "bg-red" : "bg-orange")} />
                {tile.lv === "critical" ? t("machines-machinedetail.critical") : t("machines-machinedetail.high")}
              </span>
            )}
          </div>
          <div className="mt-0.5 truncate text-[15px] leading-6 font-medium text-ink tabular-nums sm:text-[17px]" title={tile.value}>{tile.value}</div>
          <div className="truncate text-[10.5px] text-ink-3 tabular-nums" title={tile.sub}>{tile.sub ?? " "}</div>
          <div className="mt-1.5 h-1 overflow-hidden rounded-full bg-field">
            {tile.frac !== undefined && (
              <div
                className={cx("h-full rounded-full transition-[width] duration-500", tile.lv === "critical" ? "bg-red" : tile.lv === "warning" ? "bg-orange" : "bg-accent")}
                style={{ width: `${Math.max(3, Math.min(100, tile.frac))}%` }}
              />
            )}
          </div>
        </Tile>
        );
      })}
    </div>
  );
}

/** Each core now (Linux), one thin column each (iOS CoreBars); hover a column for its number. */
function CoreBars({ cores }: { cores: number[] }) {
  const busiest = Math.max(...cores);
  return (
    <section className="mt-3 rounded-card bg-surface px-4 pt-3 pb-3.5 shadow-card" role="img" aria-label={t("machines-machinedetail.cores_label", { count: cores.length, busiest: percent(busiest) })}>
      <div className="flex items-baseline gap-2">
        <h3 className="truncate text-[12.5px] font-medium text-ink-2">{t("machines-machinedetail.per_core")}</h3>
        <span className="flex-1" />
        <span className="shrink-0 text-[11.5px] whitespace-nowrap text-ink-3 tabular-nums">{t("machines-machinedetail.busiest", { value: percent(busiest) })}</span>
      </div>
      <div className={cx("mt-2.5 flex h-11 items-end", cores.length > 32 ? "gap-px" : "gap-0.5")}>
        {cores.map((c, i) => (
          <div key={i} title={t("machines-machinedetail.core_title", { n: i + 1, value: percent(c) })} className="relative h-full min-w-px flex-1 overflow-hidden rounded-[2px] bg-field">
            <div className="absolute inset-x-0 bottom-0 rounded-[2px] bg-accent transition-[height] duration-500" style={{ height: `${Math.max(4, c)}%` }} />
          </div>
        ))}
      </div>
    </section>
  );
}
