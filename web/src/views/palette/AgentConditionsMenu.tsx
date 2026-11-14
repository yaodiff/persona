// ⌘K's Filter menu (#6589), loaded with the button's first click (views/palette/AgentConditions.tsx): the Agents list's
// filter choices, each written into the query as the list's operator (`machine:`, `project:`, `state:`, `harness:`,
// `model:`, `is:pinned`, `since:`), so what is chosen stays text to read and change, the results narrow to Agent
// sessions, and "Filter Agents list" carries it to the list. The facets and the spelling are lib/agent-search.ts's.
import { useMemo, useState } from "react";
import { t } from "../../i18n";
import { stateOf } from "../../lib/format";
import { STATE_LABEL } from "../../lib/state-labels";
import { useStore } from "../../lib/store";
import type { AgentState, Session } from "../../lib/types";
import { Menu, type MenuItem } from "../../ui/atoms";

type Page = "root" | "machine" | "project" | "state" | "harness" | "model" | "since";
type Facet = Exclude<Page, "root" | "since">;
type Option = { id: string; label: string; count: number };

const STATES: AgentState[] = ["needs_you", "working", "unread", "idle", "offline"];
const WINDOWS = ["24h", "7d", "30d"] as const;
const windowLabel = (w: string) => (w === "24h" ? t("palette.since_24h") : w === "7d" ? t("palette.since_7d") : t("palette.since_30d"));
const FACETS: { page: Facet; labelKey: string }[] = [
  { page: "machine", labelKey: "lib-agent-filter.filter_machine" },
  { page: "project", labelKey: "lib-agent-filter.filter_project" },
  { page: "state", labelKey: "palette.cond_state" },
  { page: "harness", labelKey: "lib-agent-filter.filter_harness" },
  { page: "model", labelKey: "lib-agent-filter.filter_model" },
];

/** The menu over `anchor`, the query's conditions checked; a choice gives the new query to `onQuery`. */
export function AgentConditionsMenu({ anchor, query, onQuery, search, sessions, onClose }: {
  anchor: HTMLElement;
  query: string;
  onQuery: (next: string) => void;
  search: typeof import("../../lib/agent-search");
  sessions: Session[];
  onClose: () => void;
}) {
  const [page, setPage] = useState<Page>("root");
  const machines = useStore((s) => s.machines);
  const pins = useStore((s) => s.pins);
  const facets = useMemo(() => search.buildAgentFacets(sessions, machines, pins), [search, sessions, machines, pins]);
  const options = useMemo((): Record<Facet, Option[]> => {
    const spelled = (list: Option[]) => list.filter((o) => search.conditionValue(o.id));
    const states = STATES.map((st) => ({ id: st, label: STATE_LABEL[st], count: sessions.filter((s) => stateOf(s) === st).length }));
    return { machine: spelled(facets.machines), project: spelled(facets.projects), state: states, harness: spelled(facets.harnesses), model: spelled(facets.models) };
  }, [facets, search, sessions]);

  const values = (key: string) => search.conditionValues(query, key).map((v) => v.toLowerCase());
  const back: MenuItem = { key: "back", label: t("sidebar.back"), keepOpen: true, onSelect: () => setPage("root") };
  let items: MenuItem[];
  if (page === "since") {
    const chosen = values("since")[0];
    items = [
      back,
      { key: "any", label: t("lib-agent-filter.all"), divider: true, radio: true, checked: !chosen, keepOpen: true, onSelect: () => onQuery(search.dropConditions(query, "since")) },
      ...WINDOWS.map((w): MenuItem => ({ key: w, label: windowLabel(w), radio: true, checked: chosen === w, keepOpen: true, onSelect: () => onQuery(search.toggleCondition(query, "since", w, true)) })),
    ];
  } else if (page !== "root") {
    const chosen = values(page);
    const shown = options[page].filter((o) => o.count > 0 || chosen.includes(o.id.toLowerCase()));
    items = [
      back,
      { key: "all", label: t("lib-agent-filter.all"), divider: true, checked: chosen.length === 0, keepOpen: true, onSelect: () => onQuery(search.dropConditions(query, page)) },
      ...shown.map((o): MenuItem => ({ key: o.id, label: o.label, hint: String(o.count), checked: chosen.includes(o.id.toLowerCase()), keepOpen: true, onSelect: () => onQuery(search.toggleCondition(query, page, o.id)) })),
    ];
  } else {
    // a facet with one value narrows nothing: offered once there are two, or while one is chosen
    const facetRows = FACETS.filter((f) => options[f.page].filter((o) => o.count > 0).length > 1 || values(f.page).length > 0).map((f): MenuItem => {
      const n = values(f.page).length;
      return { key: f.page, label: t(f.labelKey), hint: n ? String(n) : t("lib-agent-filter.all"), submenu: true, onSelect: () => setPage(f.page) };
    });
    const since = values("since")[0];
    items = [
      ...facetRows,
      { key: "pinned", label: t("lib-agent-filter.filter_pinned"), hint: facets.pinned > 0 ? String(facets.pinned) : undefined, divider: facetRows.length > 0, checked: values("is").includes("pinned"), keepOpen: true, onSelect: () => onQuery(search.toggleCondition(query, "is", "pinned")) },
      { key: "since", label: t("palette.cond_since"), hint: since && (WINDOWS as readonly string[]).includes(since) ? windowLabel(since) : t("lib-agent-filter.all"), submenu: true, onSelect: () => setPage("since") },
      ...(search.conditionCount(query) > 0 ? [{ key: "reset", label: t("lib-agent-filter.reset_filters"), divider: true, onSelect: () => onQuery(search.dropConditions(query)) }] : []),
    ];
  }
  return <Menu anchor={anchor} onClose={onClose} align="right" width={220} header={t("palette.agent_conditions")} items={items} />;
}
