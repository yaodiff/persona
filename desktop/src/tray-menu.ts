import type { MenuItemConstructorOptions } from "electron";
import type { UpdateAction, UpdateMenuItem } from "./update-state.js";

export interface TrayLabels {
  readonly open: string;
  readonly quit: string;
}

/** Open, then the update section when there is one, then Quit. No Electron at run time, so the shape is unit-tested. */
export function trayTemplate(labels: TrayLabels, update: readonly UpdateMenuItem[], run: {
  readonly open: () => void;
  readonly quit: () => void;
  readonly update: (action: UpdateAction) => void;
}): MenuItemConstructorOptions[] {
  const section: MenuItemConstructorOptions[] = update.map(item => {
    const action = item.action;
    // A status line has no action and is shown disabled, so it reads as text rather than as a dead button.
    return action ? { label: item.label, click: () => run.update(action) } : { label: item.label, enabled: false };
  });
  const divider: MenuItemConstructorOptions[] = section.length > 0 ? [{ type: "separator" }] : [];
  return [
    { label: labels.open, click: run.open },
    { type: "separator" },
    ...section,
    ...divider,
    { label: labels.quit, click: run.quit },
  ];
}
