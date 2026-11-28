// A unified diff with old / new line numbers, green / red rows, no wrapping (scroll sideways); only the rows on
// screen are rendered (a diff can be 512 KB). With `comment`, the numbers take comments on lines (LineComment.tsx, #2437):
// a + under the pointer, the box under the diff, and `at` opens it on a line (a comment card's click).
import { useLayoutEffect, useMemo, useRef } from "react";
import { addCodeComment, commentKey, markedLines } from "../../../lib/comment-target";
import { codeQuote, diffPick, rangeLabel, type DiffScope } from "../../../lib/comments";
import { useQuotes } from "../../../lib/drafts";
import { columns, parseDiff, type DiffLine } from "../../../lib/files";
import { cx } from "../../../ui/atoms";
import { diffName } from "../../chat/CommentCard";
import { GutterNumber, LineBox, useLinePick } from "./LineComment";
import { Empty, useWindow } from "./parts";
import { t } from "../../../i18n";

const LH = 20;
const PAD = 6;

const ROW: Record<DiffLine["kind"], string> = {
  add: "bg-green-tint",
  del: "bg-red-tint",
  context: "",
  hunk: "bg-field text-ink-3",
  info: "text-ink-3 italic",
};

/** What a diff needs to take comments: whose box, which file in which diff, and where to scroll to. */
export interface DiffComment {
  mid: string;
  sid?: string;
  cwd?: string;
  /** The repository-relative path git reports for the file. */
  path: string;
  diff: DiffScope;
  /** A line to show and mark (a comment card's click): its numbers are the old file's when `old`. */
  at?: { line: number; to?: number; old?: boolean };
}

export function DiffLines({ diff, truncated, comment }: { diff: string; truncated?: boolean; comment?: DiffComment }) {
  const lines = useMemo(() => parseDiff(diff), [diff]);
  const width = useMemo(() => lines.reduce((m, l) => Math.max(m, columns(l.text)), 0), [lines]);
  const digits = useMemo(() => String(lines.reduce((m, l) => Math.max(m, l.old ?? 0, l.new ?? 0), 99)).length, [lines]);
  const scroller = useRef<HTMLDivElement>(null);
  const [start, end] = useWindow(scroller, lines.length, LH, PAD);

  const key = comment ? commentKey(comment.mid, comment.sid) : null;
  const pick = useLinePick(Boolean(key));
  const quotes = useQuotes(key ?? "").list;
  const same = (a: DiffScope, b: DiffScope) => a.kind === b.kind && (a.kind !== "commit" || (b.kind === "commit" && a.sha.slice(0, 8) === b.sha.slice(0, 8)));
  const marked = useMemo(() => {
    const here = (s: { diff?: DiffScope; path: string }) => Boolean(comment && s.diff && same(s.diff, comment.diff) && s.path === comment.path);
    return { now: markedLines(quotes, here), old: markedLines(quotes, here, true) };
  }, [quotes, comment]);
  const picked = pick.sweeping ?? pick.range;

  // opened at a line: put it a third of the way down and mark it
  const focus = useMemo(() => {
    const at = comment?.at;
    if (!at) return null;
    const num = (l: DiffLine) => (at.old ? l.old : l.new);
    const first = lines.findIndex((l) => num(l) === at.line);
    if (first < 0) return null;
    const lastLine = at.to ?? at.line;
    let last = first;
    for (let i = first; i < lines.length; i++) if ((num(lines[i]) ?? 0) <= lastLine && num(lines[i]) !== undefined) last = i;
    return { first, last };
  }, [comment?.at, lines]);
  const placed = useRef(false);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!focus || placed.current || !el) return;
    placed.current = true;
    el.scrollTop = Math.max(0, PAD + focus.first * LH - el.clientHeight / 3);
  }, [focus]);

  if (!lines.length) return <Empty icon="git" title={t("session-files-difflines.empty_title")} detail={t("session-files-difflines.empty_detail")} />;
  const num = `calc(${digits}ch + 12px)`;
  const gutter = (l: DiffLine, idx: number, side: "old" | "new") => {
    const n = side === "old" ? l.old : l.new;
    // a number takes the pick when it is the row's one for new text (old only for a removed line)
    const takes = side === "new" ? l.new !== undefined : l.new === undefined && l.old !== undefined;
    return (
      <GutterNumber
        n={n}
        at={idx}
        pick={pick}
        marked={takes ? (side === "new" ? marked.now : marked.old).get(n ?? -1) : undefined}
        enabled={Boolean(key) && takes}
        className="shrink-0 pr-1.5 text-right text-ink-3 tabular-nums select-none"
        style={{ width: num }}
      />
    );
  };
  return (
    <>
      <div ref={scroller} className="min-h-0 flex-1 overflow-auto bg-inset font-mono text-[12px]" style={{ lineHeight: `${LH}px` }} data-testid="diff">
        <div style={{ minWidth: `calc(${digits * 2}ch + ${width}ch + 24px + 2ch + 40px)`, paddingTop: PAD, paddingBottom: PAD }}>
          <div style={{ height: start * LH }} />
          {lines.slice(start, end).map((l, k) => {
            const idx = start + k;
            const lit = (picked && idx >= picked.from && idx <= picked.to) || (focus && idx >= focus.first && idx <= focus.last);
            return (
              <div key={idx} className={cx("group/row flex", ROW[l.kind], lit && "outline outline-1 -outline-offset-1 outline-accent")} style={{ height: LH }} data-row={idx}>
                {l.kind === "hunk" || l.kind === "info" ? (
                  <span className="pl-3 whitespace-pre">{l.text}</span>
                ) : (
                  <>
                    {gutter(l, idx, "old")}
                    {gutter(l, idx, "new")}
                    <span className={cx("w-[2ch] shrink-0 text-center select-none", l.kind === "add" ? "text-green" : l.kind === "del" ? "text-red" : "text-ink-3")}>
                      {l.kind === "add" ? "+" : l.kind === "del" ? "−" : ""}
                    </span>
                    <span className="pr-6 pl-1 whitespace-pre text-ink">{l.text || " "}</span>
                  </>
                )}
              </div>
            );
          })}
          <div style={{ height: (lines.length - end) * LH }} />
          {truncated && <p className="sticky left-0 px-3 pt-2 font-sans text-[11.5px] text-ink-3">{t("session-files-difflines.truncated")}</p>}
        </div>
      </div>
      {comment && key && pick.range && <DiffBox comment={comment} lines={lines} rows={pick.range} anchor={pick.anchor} onDone={pick.clear} draftKey={key} />}
    </>
  );
}

/** The floating bar for the rows picked: their line numbers and the diff's name, the code in the comment it makes. */
function DiffBox({ comment, lines, rows, anchor, draftKey, onDone }: { comment: DiffComment; lines: readonly DiffLine[]; rows: { from: number; to: number }; anchor: ReturnType<typeof useLinePick>["anchor"]; draftKey: string; onDone: () => void }) {
  const got = diffPick(lines, rows.from, rows.to);
  if (!got) return null;
  const file = comment.path;
  return (
    <LineBox
      where={`${file}:${rangeLabel(got.from, got.to)} · ${diffName(comment.diff)}`}
      anchor={anchor}
      onCancel={onDone}
      onAdd={(body) => {
        const quote = codeQuote({ file, path: comment.path, from: got.from, to: got.to, lines: got.lines, old: got.old, diff: comment.diff, lang: "diff", mid: comment.mid, sid: comment.sid, cwd: comment.cwd });
        addCodeComment(draftKey, quote, body);
        onDone();
      }}
    />
  );
}
