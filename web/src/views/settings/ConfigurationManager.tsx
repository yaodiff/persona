import { useState, type ReactNode } from "react";
import { t } from "../../i18n";
import { useChosenManager, useKnownDefault, useShownManager } from "../../lib/manager-choice";
import { managerOrder, useRoster } from "../../lib/managers";
import { Loading } from "../../ui/atoms";
import { Group, Row } from "./SettingsParts";

/**
 * Which manager a configuration editor reads and writes, independently of late requests (its children are drawn again
 * for another one). By default the current assistant (docs/assistant/managers.md "The current assistant": the current
 * assistant's settings pages have no picker of their own), drawn once the roster says which manager that is (a kept
 * pick counts before). `pick`: an editor outside those pages (the hub's download mirrors) with its own picker,
 * starting on the current assistant and kept in the address (`?manager=`).
 */
export function ConfigurationManager({ children, title, rowLabel, hint, hideSingle = false, pick = false, manager }: {
  children: (manager: string) => ReactNode;
  title?: string;
  /** A shorter label beside the picker, when the group title already names it. */
  rowLabel?: string;
  hint?: string;
  hideSingle?: boolean;
  pick?: boolean;
  manager?: string;
}) {
  if (manager) return <div key={manager}>{children(manager)}</div>;
  return pick ? <PickedManager title={title} rowLabel={rowLabel} hint={hint} hideSingle={hideSingle}>{children}</PickedManager> : <CurrentManager>{children}</CurrentManager>;
}

/** The current assistant's editor: the picked manager, else the roster's default once it is known. */
function CurrentManager({ children }: { children: (manager: string) => ReactNode }) {
  const picked = useChosenManager();
  const knownDefault = useKnownDefault();
  const manager = picked ?? knownDefault;
  if (!manager) return <Loading />;
  return <div key={manager}>{children(manager)}</div>;
}

function PickedManager({ children, title, rowLabel, hint, hideSingle }: {
  children: (manager: string) => ReactNode;
  title?: string;
  rowLabel?: string;
  hint?: string;
  hideSingle: boolean;
}) {
  const roster = useRoster();
  const knownDefault = useKnownDefault();
  const shown = useShownManager();
  const [picked, setPicked] = useState<string | null>(() => new URLSearchParams(location.search).get("manager"));
  const manager = picked ?? shown;
  const managers = roster ? managerOrder(roster) : knownDefault ? [{ id: knownDefault, name: knownDefault }] : [];
  const valid = managers.some((m) => m.id === manager);
  const label = title ?? t("settings-assistantsettings.configuration_target");
  return <>
    {(!hideSingle || managers.length > 1 || !valid) && <Group title={label} footer={hint ?? t("settings-assistantsettings.configuration_target_hint")}>
      <Row label={rowLabel ?? label}>
        <select aria-label={label} value={manager} disabled={!managers.length}
          className="min-w-0 max-w-full rounded-control bg-field px-3 py-2 text-[13px] text-ink shadow-inset-field"
          onChange={(e) => {
            const target = e.target.value;
            setPicked(target);
            const url = new URL(location.href);
            url.searchParams.set("manager", target);
            history.replaceState(history.state, "", `${url.pathname}${url.search}${url.hash}`);
          }}>
          {!valid && <option value={manager}>{manager}</option>}
          {managers.map((m) => <option key={m.id} value={m.id}>{m.name} · {m.id}</option>)}
        </select>
      </Row>
    </Group>}
    {!managers.length ? <Loading /> : !valid ? <p role="alert" className="text-[13px] text-red">{t("settings-assistantsettings.configuration_target_missing")}</p> : <div key={manager}>{children(manager)}</div>}
  </>;
}
