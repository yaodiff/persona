// ⌘K's Filter (#6589): the Agents list's filter choices at the end of the field, where they sat beside the list's own
// search field before it went. The button is all the palette (idle-tier, src/startup-preload.ts) carries: its menu
// (AgentConditionsMenu.tsx) loads with its first click. The opened palette loads the shared query helpers.
import { useEffect, useState } from "react";
import { t } from "../../i18n";
import type { Session } from "../../lib/types";
import { cx } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

/** `search`: lib/agent-search.ts once loaded; `want` asks for it. `onDone`: the menu closed (the field takes the keys again). */
export function AgentConditions({ query, onQuery, search, want, sessions, onDone }: {
  query: string;
  onQuery: (next: string) => void;
  search: typeof import("../../lib/agent-search") | null;
  want: () => void;
  sessions: Session[];
  onDone: () => void;
}) {
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const [menu, setMenu] = useState<typeof import("./AgentConditionsMenu") | null>(null);
  useEffect(() => {
    if (anchor && !menu) void import("./AgentConditionsMenu").then(setMenu, () => {}); // a failed load leaves the button
  }, [anchor, menu]);
  const open = Boolean(anchor && search && menu);
  const count = search?.conditionCount(query) ?? 0;
  const close = () => {
    setAnchor(null);
    onDone();
  };
  const label = count > 0 ? `${t("lib-agent-filter.filter")} (${count})` : t("lib-agent-filter.filter");
  return (
    <>
      <button
        type="button"
        data-testid="palette-agent-filter"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={label}
        title={label}
        onClick={(e) => {
          if (anchor) return close();
          setAnchor(e.currentTarget);
          want();
        }}
        className={cx(
          "touch-hit relative flex h-6 min-w-6 shrink-0 items-center justify-center gap-0.5 rounded-control px-1 text-meta hover:bg-hover hover:text-ink-2",
          count > 0 ? "font-semibold text-accent" : "text-ink-3",
          open && "bg-hover",
        )}
      >
        <Icon name="filter" size={14} stroke={2} />
        {count > 0 && <span className="tabular-nums leading-none">{count}</span>}
      </button>
      {open && anchor && search && menu && <menu.AgentConditionsMenu anchor={anchor} query={query} onQuery={onQuery} search={search} sessions={sessions} onClose={close} />}
    </>
  );
}
