export interface DesktopPresence {
  readonly active: boolean;
  readonly notifies: boolean;
}

/** Only an unlocked, recently used computer with notifications can claim active desktop presence. */
export function desktopPresence(idleSeconds: number, locked: boolean, notifications: boolean): DesktopPresence {
  const active = !locked && notifications && idleSeconds < 300;
  return { active, notifies: active && notifications };
}
