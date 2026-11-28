import { Fragment, useEffect, useState, type ReactNode } from "react";
import { t } from "../../i18n";
import type { Machine } from "../../lib/types";
import { fieldClass } from "../assistant/parts";

interface PickerProps {
  machines: readonly Machine[];
  initialMachineId?: string;
  selectedMid?: string;
  onSelectMachine?: (mid: string) => void;
}

export type MachineToolProps = PickerProps | { machine: Machine; initialMachineId?: string };
type RenderTool = (machine: Machine | undefined) => ReactNode;

/** A routed tool receives exactly its computer. Standalone panels still offer the picker. */
export function MachineTool(props: MachineToolProps & { children: RenderTool }) {
  if ("machine" in props) return <Fragment key={props.machine.id}>{props.children(props.machine)}</Fragment>;
  return <StandaloneTool {...props} />;
}

function StandaloneTool({ machines, initialMachineId, selectedMid, onSelectMachine, children }: PickerProps & { children: RenderTool }) {
  const [localMid, setLocalMid] = useState(initialMachineId);
  const requested = selectedMid ?? localMid;
  const select = onSelectMachine ?? setLocalMid;
  const online = machines.filter((m) => m.online);
  const choices = online.length ? online : machines;
  const machine = choices.find((m) => m.id === requested) ?? choices[0];
  const mid = machine?.id;
  useEffect(() => {
    if (mid && mid !== requested) select(mid);
  }, [mid, requested, select]);
  return (
    <div className="flex min-w-0 flex-col gap-3">
      {machine && <div><MachinePicker machine={machine} choices={choices} select={select} /></div>}
      {/* Keep reports, drafts and confirmations bound to the computer they were opened on. */}
      <Fragment key={mid}>{children(machine)}</Fragment>
    </div>
  );
}

function MachinePicker({ machine, choices, select }: {
  machine: Machine;
  choices: readonly Machine[];
  select: (mid: string) => void;
}) {
  const status = t(machine.online ? "machines.online" : "machines.offline");
  if (choices.length === 1) {
    return (
      <span data-testid="machine-chip" className="inline-flex max-w-full items-center gap-2 rounded-full bg-field px-3 py-1 text-label text-ink-2">
        <span className="truncate font-medium">{machine.name}</span>
        <span className="shrink-0 text-ink-3">{status}</span>
      </span>
    );
  }
  return (
    <label className="inline-flex max-w-full items-center gap-2 text-label text-ink-2">
      {t("machines.computer_label")}
      <select data-testid="machine-picker" className={`${fieldClass} min-h-11 min-w-0 max-w-full`} value={machine.id} onChange={(e) => select(e.target.value)}>
        {choices.map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}
      </select>
    </label>
  );
}
