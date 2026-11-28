// History of ended sessions, and direct standalone terminal pages (/terminal/<mid>/<id>) inside a single workspace card.
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { isStoppedAgent } from "../../lib/format";
import { useScopedMachines } from "../../lib/current-assistant";
import { machineWithHub } from "../../lib/mesh";
import { href, navigate, onLinkClick, type Route } from "../../lib/router";
import { openPanel } from "../../lib/panel";
import { getSession, useStore } from "../../lib/store";
import { usePanes } from "../../lib/terminal-panes";
import { buildTerminalTree, keepOnlyShown, keepRecent, paneCwd, paneHeaderInfo, sameTerminal, type OpenTerminal, type TermSpace } from "../../lib/terminals";
import { NARROW, useMedia } from "../../lib/viewport";
import { cx, IconButton } from "../../ui/atoms";
import { onNodeChanged, setKnownTree } from "../../lib/node-palette";
import { Icon } from "../../ui/icons";
import { HistoryBrowser } from "../agents/HistoryBrowser";
import { Empty } from "../session/files/parts";
import { TerminalPanel } from "../session/TerminalPanel";
import { ShellHeaderSlots } from "../ShellSwitch";
import { interactiveSessionPane } from "./work-terminal-layout";
import { t } from "../../i18n";

import { MachineTerminalEntry } from "./MachineTerminalEntry";
import type { MachineTerminalTarget } from "../../lib/machine-terminal";

type TerminalRoute = Extract<Route, { name: "terminal" }>;

export function TerminalPage({ route, onToggleSidebar, onNewAgent, detail, onWorkPane }: {
  route?: TerminalRoute;
  onToggleSidebar: () => void;
  onNewAgent: () => void;
  detail?: { selected?: { mid: string; sid: string }; render: (terminalAvailable: boolean) => ReactNode };
  onWorkPane?: (pane?: { mid: string; sid: string }) => void;
}) {
  const machines = useScopedMachines();
  const allMachines = useStore((s) => s.machines);
  const sessions = useStore((s) => s.sessions);
  const panesOn = useStore((s) => s.features.includes("terminal_panes"));
  // New terminal on a computer with no workspace yet (#6362): a hub that checks the node can (else 409 node_outdated)
  const newSpaceOn = useStore((s) => s.features.includes("pane_new_space"));
  const canOpenFolder = useStore(s => s.features.includes("pane_cwd"));
  const target: MachineTerminalTarget | undefined = route?.view === "terminals" && !route.id ? history.state?.machineTerminal : undefined;
  const addressed = target?.mid ?? route?.mid ?? detail?.selected?.mid;
  // A bookmarked terminal or session may name another assistant's computer. Only the current one's are listed.
  const targets = addressed && !machines.some((m) => m.id === addressed) ? allMachines.filter((m) => machines.includes(m) || m.id === addressed) : machines;
  const shown: OpenTerminal | undefined = route?.mid && route.id ? { mid: route.mid, id: route.id } : undefined;
  const selected = detail?.selected;
  const { listing, refresh, reload } = usePanes(targets, panesOn && Boolean(shown || detail || target));
  // a stopped Agent (#4754) has its terminal too: the shell it left, or Resume when that is gone
  const stoppedSelected = useStore(() => Boolean(selected && isStoppedAgent(getSession(selected.mid, selected.sid))));
  const terminalAvailable = Boolean(panesOn && selected && targets.some((m) => m.id === selected.mid && m.online) &&
    (interactiveSessionPane(listing[selected.mid], selected.sid) || stoppedSelected));
  const mid = selected?.mid, sid = selected?.sid;
  useEffect(() => {
    onWorkPane?.(terminalAvailable && mid && sid ? { mid, sid } : undefined);
    return () => onWorkPane?.();
  }, [mid, sid, onWorkPane, terminalAvailable]);
  const hasDetail = Boolean(detail);
  const narrow = useMedia(NARROW);
  const hideList = hasDetail && narrow;
  const browse: TerminalRoute = { name: "terminal" };

  const trees = useMemo(() => {
    const out: Record<string, TermSpace[]> = {};
    for (const m of targets) out[m.id] = buildTerminalTree(listing[m.id]?.data, sessions[m.id] ?? []);
    return out;
  }, [targets, listing, sessions]);

  // Node commands in the palette still see all panes when a terminal or work session is open.
  useEffect(() => {
    if (!panesOn || (!shown && !detail)) return;
    for (const m of targets) setKnownTree(m.id, trees[m.id] ?? []);
  }, [panesOn, shown, detail, targets, trees]);
  useEffect(() => (panesOn && Boolean(shown || detail) ? onNodeChanged(() => void refresh()) : undefined), [panesOn, shown, detail, refresh]);

  // Keep standalone terminals mounted as before; switching a session's content is handled by ChatView itself.
  const [open, setOpen] = useState<OpenTerminal[]>([]);
  const shownKey = shown ? `${shown.mid}\n${shown.id}` : "";
  useEffect(() => {
    if (shown) setOpen((list) => keepRecent(list, shown));
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the key describes the open terminal
  }, [shownKey]);
  const current = useRef(shown);
  current.current = shown;
  useEffect(() => {
    const hide = () => document.visibilityState === "hidden" && setOpen((list) => keepOnlyShown(list, current.current));
    document.addEventListener("visibilitychange", hide);
    return () => document.removeEventListener("visibilitychange", hide);
  }, []);
  const opened = useRef(new Map<string, number>());
  const stable = [...open].sort((a, b) => {
    for (const item of [a, b]) if (!opened.current.has(`${item.mid}\n${item.id}`)) opened.current.set(`${item.mid}\n${item.id}`, opened.current.size);
    return opened.current.get(`${a.mid}\n${a.id}`)! - opened.current.get(`${b.mid}\n${b.id}`)!;
  });
  const close = (item: OpenTerminal) => {
    setOpen((list) => list.filter((x) => !sameTerminal(x, item)));
    if (sameTerminal(item, shown)) navigate(browse);
  };

  // Direct standalone terminal route (/terminal/<mid>/<id>): renders inside a single workspace card with one compact 44px bar.
  if (shown && !detail) {
    return (
      <div data-terminal-workspace className="relative flex h-full min-w-0 flex-1 flex-col">
        {panesOn ? stable.map((item) => {
          const on = sameTerminal(item, shown);
          const spaces = trees[item.mid] ?? [];
          const cwd = paneCwd(spaces, item.id);
          const info = paneHeaderInfo(spaces, item.id);
          const machineName = machineWithHub(item.mid, targets);
          const context = [info.workspace, machineName].filter(Boolean).join(" / ");
          return (
            <div key={`${item.mid}\n${item.id}`} inert={!on || undefined} className={cx("absolute inset-0 flex flex-col", !on && "pointer-events-none invisible")}>
              <TerminalPanel
                standalone
                onToggleSidebar={onToggleSidebar}
                mid={item.mid}
                sid={item.id}
                label={info.title || undefined}
                context={context || undefined}
                cwd={cwd}
                onFiles={() => openPanel({ kind: "files", mid: item.mid, sid: item.id.startsWith("pane:") ? "" : item.id, cwd })}
                onClose={() => close(item)}
              />
            </div>
          );
        }) : (
          <Empty icon="terminal" title={t("work-entry.terminal_unavailable")} />
        )}
      </div>
    );
  }

  if (target && panesOn) return <MachineTerminalEntry key={JSON.stringify(target)} target={target}
    machine={targets.find(m => m.id === target.mid)} listing={listing[target.mid]}
    canCreateSpace={newSpaceOn} canOpenFolder={canOpenFolder} retry={() => void reload([target.mid])} />;

  return (
    <div className="flex h-full min-w-0 flex-col">
      <header className={cx("flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4", hasDetail && "max-lg:hidden")}>
        <h1 className="hidden min-w-0 flex-1 truncate text-[14px] font-semibold text-ink lg:block">{t("work-entry.title")}</h1>
        <ShellHeaderSlots page="agents" onToggleSidebar={onToggleSidebar} endWide end={<IconButton icon="plus" label={t("agentspage.new_agent")} onClick={onNewAgent} />} />
      </header>
      {hasDetail && (
        <a href={href(browse)} onClick={(e) => onLinkClick(e, browse)} className="touch-hit flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 text-[13px] text-accent-ink lg:hidden">
          <Icon name="arrowLeft" size={14} />{t("work-entry.back")}
        </a>
      )}
      <div className="relative flex min-h-0 flex-1">
        <section aria-label={t("work-entry.sessions")} inert={hideList || undefined} className={cx("flex min-h-0 w-full min-w-0 flex-col lg:w-[320px] lg:shrink-0 lg:border-r lg:border-line", hasDetail && "max-lg:invisible max-lg:absolute max-lg:inset-0")}>
          <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-2 pb-3">
            <HistoryBrowser activeSession={selected ? { machineId: selected.mid, sessionId: selected.sid } : undefined} />
          </div>
        </section>
        <section aria-label={t("work-entry.content")} className={cx("relative flex min-h-0 min-w-0 flex-1 flex-col", !hasDetail && "max-lg:hidden")}>
          {detail ? detail.render(terminalAvailable)
            : <Empty icon="chat" title={t("work-entry.pick_session")} detail={t("work-entry.pick_session_detail")} />}
        </section>
      </div>
    </div>
  );
}
