import { useState } from "react";
import { t } from "../i18n";
import { storedValue, storeValue } from "../lib/compatibility";
import { hubLabel } from "../lib/mesh";
import { href, onLinkClick } from "../lib/router";
import type { Machine } from "../lib/types";
import { cx } from "../ui/atoms";
import { Icon } from "../ui/icons";
import { Segmented } from "./assistant/parts";

export type OverviewMachineView = "cards" | "rows";
export const OVERVIEW_MACHINE_VIEW_KEY = "persona.overview.machines.view";

/** The browser's Computers display choice, like the Machines and Files view preferences. Cards are the default. */
export function useOverviewMachineView(): [OverviewMachineView, (view: OverviewMachineView) => void] {
  const [view, setView] = useState<OverviewMachineView>(() => {
    try {
      return storedValue(OVERVIEW_MACHINE_VIEW_KEY) === "rows" ? "rows" : "cards";
    } catch {
      return "cards";
    }
  });
  const choose = (next: OverviewMachineView) => {
    try {
      storeValue(OVERVIEW_MACHINE_VIEW_KEY, next);
    } catch {
      // A page-only choice still works when browser storage is disabled or full.
    }
    setView(next);
  };
  return [view, choose];
}

export function OverviewMachineViewSwitch({ value, onChange }: { value: OverviewMachineView; onChange: (view: OverviewMachineView) => void }) {
  return (
    <Segmented
      compact
      value={value}
      onChange={onChange}
      label={t("machines.view_label")}
      options={[{ v: "cards", label: t("machines.view_cards") }, { v: "rows", label: t("home.view_rows") }]}
    />
  );
}

/** The Overview's compact computer rows. This mode mounts no metrics reader. */
export function OverviewMachineRows({ machines, agentCounts, offlineText }: {
  machines: Machine[];
  agentCounts?: ReadonlyMap<string, number>;
  offlineText: (machine: Machine) => string;
}) {
  return (
    <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 @min-[60rem]:grid-cols-3 @min-[84rem]:grid-cols-4 @min-[110rem]:grid-cols-5">
      {machines.map((m) => {
        const count = agentCounts && (agentCounts.get(m.id) ?? 0);
        const line = m.online ? [count !== undefined && t("home.machine_agents", { count }), m.system?.name].filter(Boolean).join(" · ") || t("home.online") : offlineText(m);
        const hub = hubLabel(m, machines);
        const detail = { name: "machine" as const, mid: m.id };
        return (
          <a key={m.id} href={href(detail)} onClick={(e) => onLinkClick(e, detail)} className="flex items-center gap-3 rounded-card bg-surface px-3 py-2.5 shadow-card card-interactive">
            <span className={cx("flex size-8 shrink-0 items-center justify-center rounded-[8px]", m.online ? "bg-field text-ink-2" : "bg-inset text-ink-3")}>
              <Icon name="machine" size={15} />
            </span>
            <span className="min-w-0 flex-1">
              <span className="flex min-w-0 items-center gap-1.5">
                <span className="truncate text-meta font-medium text-ink">{m.name}</span>
                {hub && <span className="shrink-0 truncate text-submeta text-ink-3">{hub}</span>}
              </span>
              <span className="block truncate text-submeta text-ink-3" title={!m.online && m.error && offlineText(m) !== m.error ? m.error : undefined} data-machine-line>{line}</span>
            </span>
            <span className={cx("size-2 shrink-0 rounded-full", m.online ? "bg-green" : "bg-line-strong")} title={m.online ? t("home.online") : t("home.offline")} />
          </a>
        );
      })}
    </div>
  );
}
