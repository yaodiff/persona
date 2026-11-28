// The pieces of a trace other than tool calls (those are ActionRow.tsx): notices (a background task reporting back,
// a wake, a plain line), the compaction divider, pictures and their viewer.
import { useState } from "react";
import { imageURL } from "../../lib/api";
import { compactionLabel } from "../../lib/compaction";
import { clock } from "../../lib/format";
import { openPanel, useReadOnlyFile } from "../../lib/panel";
import { getSession } from "../../lib/store";
import { taskLabel, taskPill, taskSubject, wakeSummary, type Tone } from "../../lib/tasks";
import type { Item } from "../../lib/types";
import { Collapse, copyText, cx } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { Markdown } from "../../ui/Markdown";
import { FoldedReport } from "./FoldedReport";
import { useChatContext } from "./chat-context";
import { ChatImageViewer } from "./LazyImageViewer";
import { t } from "../../i18n";

function firstLine(s?: string) {
  return (s ?? "").trim().split("\n")[0].slice(0, 160);
}

function Disclosure({
  icon,
  filled,
  label,
  chip,
  mono,
  extra,
  status,
  detail,
  onChip,
  chipTitle,
  items,
}: {
  icon: IconName;
  filled?: boolean;
  label: string;
  chip?: string;
  mono?: boolean;
  /** The chip is a link (a tool's file): it opens that instead of folding the row. */
  onChip?: () => void;
  chipTitle?: string;
  extra?: React.ReactNode;
  status?: React.ReactNode;
  detail?: React.ReactNode;
  /** The items it draws (search-reveal.ts finds a hit by them). */
  items?: string;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div data-items={items}>
      <button
        type="button"
        aria-expanded={open}
        disabled={!detail}
        onClick={() => setOpen((o) => !o)}
        className="group/row -mx-[3px] flex h-7 w-[calc(100%+6px)] min-w-0 items-center gap-2 rounded-control px-[3px] text-left transition-colors duration-100 enabled:hover:bg-hover-2"
      >
        <span className="relative flex size-4 shrink-0 items-center justify-center text-ink-3">
          <Icon name={icon} size={13} className={cx("transition-opacity duration-100", detail ? "group-hover/row:opacity-0" : "", open && "opacity-0")} style={filled ? { fill: "currentColor", stroke: "none" } : undefined} />
          {detail && (
            <Icon
              name="chevron"
              size={12}
              stroke={2.2}
              className={cx("absolute transition-[opacity,transform] duration-150 group-hover/row:opacity-100", open ? "opacity-100" : "opacity-0")}
              style={{ transform: open ? "none" : "rotate(-90deg)" }}
            />
          )}
        </span>
        <span className="shrink-0 text-[12.5px] font-medium whitespace-nowrap text-ink">{label}</span>
        {chip && (
          <span
            {...(onChip && {
              role: "link",
              tabIndex: 0,
              title: chipTitle,
              onClick: (e: React.MouseEvent) => {
                e.stopPropagation();
                onChip();
              },
              onKeyDown: (e: React.KeyboardEvent) => {
                if (e.key !== "Enter" && e.key !== " ") return;
                e.preventDefault();
                e.stopPropagation();
                onChip();
              },
            })}
            className={cx(
              "inline-flex h-5.5 min-w-0 items-center truncate rounded-chip bg-hover-2 px-1.5 text-[11.5px] text-ink-2 shadow-hairline dark:bg-field",
              mono && "font-mono",
              onChip && "cursor-pointer underline-offset-2 hover:text-accent-ink hover:underline dark:hover:text-accent-ink",
            )}
          >
            <span className="truncate">{chip}</span>
          </span>
        )}
        {extra}
        <span className="ml-auto flex shrink-0 items-center pl-1">{status}</span>
      </button>
      {detail && (
        <Collapse open={open} lazy>
          <div className="mt-0.5 mb-1.5 ml-2 border-l border-line py-0.5 pl-3.5">{detail}</div>
        </Collapse>
      )}
    </div>
  );
}


function Mono({ text, tone }: { text: string; tone?: "out" | "error" }) {
  return (
    <div className="group/mono relative">
      <pre
        className={cx(
          "max-h-72 overflow-auto rounded-[8px] px-2.5 py-2 font-mono text-[11.5px] leading-[1.6] whitespace-pre-wrap [overflow-wrap:anywhere] shadow-hairline",
          tone === "error" ? "bg-red-tint text-red" : "bg-inset text-ink-2",
        )}
      >
        {text}
      </pre>
      <button
        type="button"
        aria-label={t("chat-steps.copy")}
        onClick={() => copyText(text)}
        className="absolute top-1 right-1 flex size-6 touch-hit items-center justify-center rounded-[6px] bg-surface text-ink-3 opacity-0 shadow-btn transition-opacity group-hover/mono:opacity-100 hover:text-ink focus-visible:opacity-100 [@media(hover:none)]:opacity-100"
      >
        <Icon name="copy" size={12} />
      </button>
    </div>
  );
}


const TONE: Record<Tone, string> = {
  green: "bg-green-tint text-green",
  red: "bg-red-tint text-red",
  neutral: "bg-field text-ink-2",
  orange: "bg-orange-tint text-orange",
};

const TASK_ICON: Record<string, IconName> = { shell: "run", monitor: "monitor", agent: "agent" };

/** A notice in a trace: a background task reporting back (iOS TaskNoteRow), a wake, or a plain line. */
/** Where the context was compacted (lib/compaction.ts): a hairline across the chat, what is known about it in the middle;
 *  the notices folded into it show on hover. */
export function CompactDivider({ item }: { item: Item }) {
  const label = compactionLabel(item.compact);
  const notes = [...(item.compact?.notes ?? []), item.ts ? clock(item.ts) : ""].filter(Boolean).join("\n");
  return (
    <div role="separator" aria-label={label} title={notes || undefined} className="flex min-w-0 items-center gap-3 py-1 text-[11.5px] text-ink-3" data-compact-divider>
      <span aria-hidden className="h-px min-w-4 flex-1 bg-line-strong" />
      <span className="min-w-0 truncate whitespace-nowrap">{label}</span>
      <span aria-hidden className="h-px min-w-4 flex-1 bg-line-strong" />
    </div>
  );
}

export function NoticeRow({ item, mid, sid }: { item: Item; mid: string; sid: string }) {
  const { tasks, jumpToCall } = useChatContext();
  const readOnly = useReadOnlyFile();
  if (item.compact) return <CompactDivider item={item} />;
  const task = item.task;
  if (task) {
    const call = tasks.calls.get(item.id);
    const pill = taskPill(task, call ? tasks.startedAt.get(call) : undefined, item.ts);
    const out = task.output_file;
    const detail = (
      <div className="flex flex-col gap-1.5 py-1">
        {task.kind === "shell" ? (
          <Mono text={`$ ${task.detail || task.title || ""}`} />
        ) : task.kind === "agent" ? (
          task.detail && (
            <FoldedReport id={`notice:${item.id}`} text={task.detail} className="text-[12px] leading-relaxed">
              <Markdown text={task.detail} small mid={mid} cwd={getSession(mid, sid)?.cwd} />
            </FoldedReport>
          )
        ) : (
          task.detail && <Mono text={task.detail} />
        )}
        {task.kind === "monitor" && (task.status === "event" || !task.detail) && task.title && <p className="text-[12px] text-ink-3">{task.title}</p>}
        {out && (
          <button
            type="button"
            onClick={() => openPanel({ kind: "file", mid, path: out, cwd: getSession(mid, sid)?.cwd, ...(readOnly ? { readOnly: true } : {}) })}
            className="flex w-fit items-center gap-1.5 rounded-[6px] px-0.5 text-[12px] font-medium whitespace-nowrap text-accent-ink hover:underline"
          >
            <Icon name="read" size={12} />
            {t("chat-steps.view_output")}
          </button>
        )}
        {call && jumpToCall && (
          <button
            type="button"
            onClick={() => jumpToCall(call)}
            className="flex w-fit items-center gap-1.5 rounded-[6px] px-0.5 text-[12px] font-medium whitespace-nowrap text-accent-ink hover:underline"
            data-jump-to-call
          >
            <Icon name="up" size={12} />
            {t("chat-steps.jump_to_call")}
          </button>
        )}
      </div>
    );
    return (
      <Disclosure
        icon={TASK_ICON[task.kind] ?? "bell"}
        label={taskLabel(task)}
        chip={taskSubject(task, call ? tasks.descriptions.get(call) : undefined) || firstLine(item.text)}
        mono={task.kind === "shell"}
        status={pill ? <span className={cx("rounded-[4px] px-1 text-[10.5px] font-medium whitespace-nowrap", TONE[pill.tone])}>{pill.text}</span> : null}
        detail={task.detail || task.title || out || (call && jumpToCall) ? detail : undefined}
        items={item.id}
      />
    );
  }
  if (item.wake) {
    const w = wakeSummary(item);
    return (
      <div className="flex min-h-7 items-center gap-2 px-[3px] py-1 text-[12.5px] text-ink-2" data-items={item.id}>
        <Icon name="bell" size={13} className={cx("shrink-0", w.failed ? "text-red" : "text-accent")} />
        <span className="min-w-0 truncate">{w.text}</span>
      </div>
    );
  }
  return (
    <div className="flex min-h-7 items-start gap-2 px-[3px] py-1 text-[12.5px] text-ink-2" data-items={item.id}>
      <Icon name="bell" size={13} className="mt-[3px] shrink-0 text-ink-3" />
      <span className="min-w-0 leading-relaxed whitespace-pre-wrap">{item.text}</span>
    </div>
  );
}

export function imageSrc(item: Item, mid: string, sid: string) {
  if (item.image?.data) return `data:${item.image.mime};base64,${item.image.data}`;
  return imageURL(mid, sid, item.id);
}

export function Thumb({ item, mid, sid, size = 132 }: { item: Item; mid: string; sid: string; size?: number }) {
  const [open, setOpen] = useState(false);
  const src = imageSrc(item, mid, sid);
  const w = item.image?.width,
    h = item.image?.height;
  const ratio = w && h ? w / h : 1;
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="block overflow-hidden rounded-[10px] bg-field shadow-hairline transition-transform duration-150 active:scale-[0.98]"
        // a phone screenshot (tall) gets more height, so it is not a sliver
        style={ratio < 1 ? { width: Math.round(size * 1.5 * ratio), height: Math.round(size * 1.5) } : { width: Math.round(Math.min(size * 1.6, size * ratio)), height: size }}
        aria-label={t("chat-steps.view_image")}
      >
        <img src={src} alt="" loading="lazy" decoding="async" className="size-full object-cover" />
      </button>
      {open && <ChatImageViewer item={item} mid={mid} sid={sid} onClose={() => setOpen(false)} />}
    </>
  );
}
