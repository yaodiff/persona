import { sameValue } from "./stable-lines";
import { workspaceKey, type HostWorkspace, type WorkspaceCatalogList, type WorkspaceCatalogSnapshot } from "./workspace-catalog";

/** Reconcile a discovery response with its manager's last inventory. Only catalog readers load this code. */
export function reconcileInventory(previous: WorkspaceCatalogSnapshot, result: WorkspaceCatalogList): WorkspaceCatalogSnapshot {
  const before = new Map(previous.workspaces.map((w) => [workspaceKey(w, previous.owner, previous.manager), w]));
  const unique = new Map<string, HostWorkspace>();
  for (const w of result.workspaces) {
    if (!w.machine_id || !w.source || !w.source_id) continue;
    const key = workspaceKey(w, previous.owner, previous.manager);
    const old = before.get(key);
    unique.set(key, old && sameValue(old, w) ? old : w);
  }
  // Some older managers return partial inventories without cached failed machines. Keep those rows until a
  // successful discovery answers for that machine; a successful empty inventory still removes vanished rows.
  const failed = result.errors ?? [];
  for (const [key, old] of before) {
    if (!unique.has(key) && failed.some((e) => e.machine_id === old.machine_id && (!e.source || e.source === old.source))) unique.set(key, old);
  }
  // Discovery responses may complete in machine order rather than display order. A reorder is not a change.
  const next = [...unique].sort(([a], [b]) => a.localeCompare(b)).map(([, w]) => w);
  const failedMachines = [...new Set(failed.map(e => e.machine_id))].sort();
  if (sameValue(failedMachines, previous.failedMachines) && next.length === previous.workspaces.length && next.every((w, i) => w === previous.workspaces[i])) return previous;
  return { ...previous, workspaces: next, failedMachines };
}
