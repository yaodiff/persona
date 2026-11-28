import { navigate } from "./router";

/** Only the terminal inventory entry reached from a computer carries this context. */
export interface MachineTerminalTarget {
  mid: string;
  cwd?: string;
  host?: string;
  workspaceId?: string;
}

export function openMachineTerminal(target: MachineTerminalTarget) {
  navigate({ name: "terminal", view: "terminals" }, false, { machineTerminal: target });
}
