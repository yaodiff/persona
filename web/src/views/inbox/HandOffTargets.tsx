// The Agents work can be handed to and the row each is picked by, shared by the Inbox's Hand to AI and a meeting's Hand
// to Agent (#8852), so both offer the same Agents the same way.
import { activityOf } from "../../lib/format";
import { byTime } from "../../lib/times";
import type { Machine, Session } from "../../lib/types";
import { cx } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

/** A hand-off target's line under its title. */
export const TARGET_SUB = "block truncate text-submeta text-ink-3";

/** The agents Hand to AI offers, latest first: not Persona's own, on computers online now, not offline. */
export function handOffAgents(sessions: Record<string, Session[]>, machines: Machine[]): Session[] {
  const online = new Set(machines.filter((m) => m.online).map((m) => m.id));
  return Object.values(sessions).flat()
    .filter((s) => !s.assistant && online.has(s.machine_id) && activityOf(s) !== "offline")
    .sort((a, b) => byTime(b.updated_at, a.updated_at));
}

/** One target to pick, checked when picked; `badge` follows its title (Hand to AI's "Last time"). */
export function Target({ on, badge, onPick, title, sub, icon }: { on: boolean; badge?: React.ReactNode; onPick: () => void; title: string; sub?: string; icon: React.ReactNode }) {
  return (
    <button type="button" role="radio" aria-checked={on} onClick={onPick} className={cx("flex w-full items-center gap-2.5 px-3 py-2 text-left transition-colors", on ? "bg-accent-tint" : "hover:bg-hover")}>
      {icon}
      <span className="min-w-0 flex-1">
        <span className="flex min-w-0 items-center gap-1.5">
          <span className="truncate text-meta text-ink">{title}</span>
          {badge}
        </span>
        {sub && <span className={TARGET_SUB}>{sub}</span>}
      </span>
      <Icon name="check" size={13} stroke={2.6} className={cx("shrink-0 text-accent-ink", !on && "invisible")} />
    </button>
  );
}
