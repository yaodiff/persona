import { enc } from "./api";
import { BUILTIN_MANAGER } from "./manager-choice";
import type { ManagerList } from "./managers";
import type { ScopedAssistantClient } from "./scoped-assistant-api";
import { listExtensionTrust, setExtensionTrust, type ExtensionTrust } from "./tracks";

const path = "/assistant/extension-trust";

/** The owning assistant's advertised capability is independent of the Settings layout selected in Labs. */
export function extensionInfoAccessMode(roster: ManagerList | null, managerId: string): "scoped" | "legacy" | undefined {
  const manager = roster?.managers.find((m) => m.id === managerId);
  if (manager?.extension_info_access_scoped === true) return "scoped";
  if (managerId === (roster?.default ?? BUILTIN_MANAGER)) return "legacy";
}

export async function listScopedExtensionTrust(client: ScopedAssistantClient): Promise<ExtensionTrust[] | null> {
  const result = await client.request<{ items?: ExtensionTrust[] }>(path, undefined, async () => ({ items: await listExtensionTrust() ?? undefined }));
  const items = result?.items;
  if (!Array.isArray(items) || !items.every((row) => row && typeof row.extension === "string" &&
    (row.trust === "owner" || row.trust === "third_party") &&
    (row.categories == null || Array.isArray(row.categories) && row.categories.every((c) => typeof c === "string")))) return null;
  return items;
}

export function setScopedExtensionTrust(client: ScopedAssistantClient, id: string, trust: ExtensionTrust): Promise<ExtensionTrust> {
  return client.request<ExtensionTrust>(`${path}/${enc(id)}`, { method: "PUT", json: { trust: trust.trust, categories: trust.categories } },
    () => setExtensionTrust(id, trust));
}
