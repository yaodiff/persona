import { useEffect, useId, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { t } from "../i18n";
import { agentId, agentName } from "../lib/agent-kind";
import { awaitsReply, justFinished, replyAsk, sessionSource, sourceName } from "../lib/agent-groups";
import { fetchMessages, mergeMessages, personalAgentDraftKey, providerLabel, type AgentSession } from "../lib/agents";
import { sessionHarnessLabel } from "../lib/agent-chatlist-filter";
import { storedPageDraft } from "../lib/drafts";
import { backgroundWaiting, sessionCan, sessionPreview, sourceSubtitle, stateOf } from "../lib/format";
import { machineWithHub } from "../lib/mesh";
import { plainMarkdown } from "../lib/plain-markdown";
import { launcherTitle, useProviderNames } from "../lib/provider-names";
import { exactTime, relativeTime } from "../lib/relative-time";
import { draftLine, rowLine } from "../lib/row-line";
import { modelLabel } from "../lib/session-models";
import { stateName } from "../lib/state-labels";
import { useStore } from "../lib/store";
import { rowCards } from "../lib/supervisor-held";
import type { Session } from "../lib/types";
import { NAV_RAIL_METRICS } from "../ui/nav-rail-metrics";
import { StatusMark } from "../ui/StatusMark";
import type { RowHover, RowHoverDetail } from "./SidebarRowHover";

function Detail({ label, children }: { label: string; children: ReactNode }) {
  if (!children) return null;
  return <>
    <dt className="text-ink-3">{label}</dt>
    <dd className="min-w-0 wrap-anywhere text-ink-2">{children}</dd>
  </>;
}

function Activity({ at }: { at?: string }) {
  const exact = exactTime(at).slice(0, -3);
  if (!exact) return null;
  return <Detail label={t("sidebar-row-hover.active")}>
    <time dateTime={at} className="tabular-nums">{relativeTime(at, Date.now(), true)} <span className="text-ink-3">· <span className="whitespace-nowrap">{exact}</span></span></time>
  </Detail>;
}

function CardContent({ title, titleDetail, preview, children }: { title: string; titleDetail?: string; preview?: string; children: ReactNode }) {
  return <>
    <p className="wrap-anywhere text-sm font-semibold leading-snug text-ink">{title}</p>
    {titleDetail && <p className="mt-1 wrap-anywhere text-submeta text-ink-3">{titleDetail}</p>}
    {preview && <p data-sidebar-hover-preview className="mt-2 line-clamp-3 wrap-anywhere text-meta leading-snug text-ink-2">{preview}</p>}
    <dl className="mt-3 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 border-t border-line pt-3 text-submeta leading-snug">{children}</dl>
  </>;
}

function Launcher({ session, launcher, tool }: { session: Session; launcher: string; tool: string }) {
  const names = useProviderNames(session.manager);
  const title = launcherTitle(launcher, names);
  if (tool && title === launcher && agentId(launcher) === agentId(session.agent)) return tool;
  const label = title === launcher ? agentName(launcher) : title;
  return <>{tool && tool.toLowerCase() !== label.toLowerCase() ? `${tool} · ${label}` : label}</>;
}

function SessionDetails({ detail, row }: { detail: Extract<RowHoverDetail, { session: Session }>; row: HTMLElement }) {
  const { session: s } = detail;
  const { machine, ix, awaiting, finished } = useStore((store) => {
    const machine = s.machine_id ? machineWithHub(s.machine_id, store.machines) : "";
    const { ix } = rowCards(store.interactions, s.machine_id, s.session_id);
    return { machine, ix, awaiting: awaitsReply(s, store.mutes), finished: justFinished(s, store.mutes) };
  });
  const state = s.view ? stateOf(s) : ix ? "needs_you" : detail.working ? "working" : stateOf(s);
  const waiting = backgroundWaiting(s);
  const status = state === "idle" && awaiting ? "awaits_reply" : state === "idle" && finished ? "just_finished" : state === "working" && waiting ? "bg_waiting" : state;
  const draft = detail.draft ?? draftLine({ text: storedPageDraft(`${s.machine_id}/${s.session_id}`), quoted: false });
  const line = draft ?? (s.view ? rowLine(s, ix) : ix && rowLine({ ...s, state: "needs_you" }, ix));
  const question = state === "needs_you" || awaiting ? replyAsk(s) : "";
  const preview = line ? `${line.lead ?? ""}${line.text}` : plainMarkdown(question || sessionPreview(s));
  const tool = s.agent || s.agent_label || s.agent_name || s.source_kind?.startsWith("codex_") ? sessionHarnessLabel(s) : "";
  const launcher = (s.launcher || s.model_pick?.launcher)?.trim();
  const model = (s.model || s.model_pick?.model)?.trim();
  const source = sessionSource(s);
  // The origin mark already knows whether the extension's icon and name are ready.
  const origin = row.querySelector("[data-origin-mark]")?.getAttribute("aria-label");
  const sourceDetail = s.source_kind && !sessionCan(s, "send") ? sourceSubtitle(s) : origin || (source && sourceName(source));
  const workspace = s.workspace_cwd || s.cwd || s.worktree || s.space_name;
  return <CardContent title={detail.title} titleDetail={detail.titleDetail} preview={preview}>
    <Detail label={t("sidebar-row-hover.status")}>
      <span className="flex items-center gap-1.5"><StatusMark state={state} size="sm" label={false} awaiting={awaiting} finished={finished} waiting={waiting} />{stateName(status)}</span>
    </Detail>
    <Detail label={t("sidebar-row-hover.tool")}>{launcher ? <Launcher session={s} launcher={launcher} tool={tool} /> : tool}</Detail>
    <Detail label={t("sidebar-row-hover.model")}>{model && modelLabel(model)}</Detail>
    <Activity at={s.updated_at} />
    <Detail label={t("sidebar-row-hover.computer")}>{machine}</Detail>
    <Detail label={t("sidebar-row-hover.workspace")}>{workspace}</Detail>
    <Detail label={t("sidebar-row-hover.branch")}>{s.branch}</Detail>
    <Detail label={t("sidebar-row-hover.tab")}>{detail.tab}</Detail>
    <Detail label={t("sidebar-row-hover.source")}>{sourceDetail}</Detail>
    <Detail label={t("sidebar-row-hover.todo")}>{s.todo?.title}</Detail>
  </CardContent>;
}

function PersonalDetails({ title, agent }: { title: string; agent: AgentSession }) {
  const draft = draftLine({ text: storedPageDraft(personalAgentDraftKey(agent.id)), quoted: false });
  const hasDraft = Boolean(draft);
  const [latest, setLatest] = useState("");
  useEffect(() => {
    if (hasDraft) return;
    const controller = new AbortController();
    void fetchMessages(agent.id, undefined, 5, controller.signal).then(({ messages }) => {
      if (controller.signal.aborted) return;
      const preview = mergeMessages([], messages).reverse()
        .map((m) => plainMarkdown(m.blocks.filter((b) => b.type === "text").map((b) => b.text).join(" ")))
        .find(Boolean);
      setLatest(preview ?? "");
    }).catch(() => {});
    return () => controller.abort();
  }, [agent.id, hasDraft]);
  const at = new Date(agent.rankTimeMs);
  return <CardContent title={title} preview={draft ? draft.lead + draft.text : latest}>
    <Detail label={t("sidebar-row-hover.status")}>{stateName(agent.unreadCount > 0 ? "unread" : "idle")}</Detail>
    <Detail label={t("sidebar-row-hover.source")}>{providerLabel(agent.provider)}</Detail>
    <Activity at={agent.rankTimeMs > 0 && Number.isFinite(at.getTime()) ? at.toISOString() : undefined} />
  </CardContent>;
}

const { previewGap: gap, previewWidth } = NAV_RAIL_METRICS;

export default function SidebarRowHoverCard({ hover: { row, sidebar, detail }, onClose }: { hover: RowHover; onClose: () => void }) {
  const id = useId();
  const panel = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const card = panel.current!;
    const place = () => {
      const anchor = row.getBoundingClientRect();
      if (!row.isConnected || !anchor.height || row.closest("[inert]")) return onClose();
      const screenW = document.documentElement.clientWidth || window.innerWidth;
      const screenH = document.documentElement.clientHeight || window.innerHeight;
      const right = sidebar.getBoundingClientRect().right;
      const room = screenW - right - gap * 2;
      const width = Math.min(previewWidth, room >= previewWidth / 2 ? room : screenW - gap * 2);
      card.style.width = `${width}px`;
      card.style.maxHeight = `${screenH - gap * 2}px`;
      card.style.left = `${Math.max(gap, Math.min(right + gap, screenW - width - gap))}px`;
      card.style.top = `${Math.max(gap, Math.min(anchor.top, screenH - card.offsetHeight - gap))}px`;
    };
    place();
    const previous = row.getAttribute("aria-describedby");
    const description = previous ? `${previous} ${id}` : id;
    row.setAttribute("aria-describedby", description);
    const resize = new ResizeObserver(place);
    resize.observe(card);
    resize.observe(row);
    resize.observe(sidebar);
    return () => {
      resize.disconnect();
      if (row.getAttribute("aria-describedby") !== description) return;
      if (previous) row.setAttribute("aria-describedby", previous);
      else row.removeAttribute("aria-describedby");
    };
  }, [id, row, sidebar, onClose]);

  return createPortal(
    <div ref={panel} id={id} role="tooltip" data-sidebar-row-hover-card className="pointer-events-none fixed z-40 overflow-hidden rounded-nav-preview border border-line bg-surface p-4 text-left shadow-nav-preview">
      {"session" in detail ? <SessionDetails detail={detail} row={row} /> : <PersonalDetails title={detail.title} agent={detail.agent} />}
    </div>,
    document.body,
  );
}
