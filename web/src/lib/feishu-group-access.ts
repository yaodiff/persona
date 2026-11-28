// Settings › Connectors › Lark bot › Group access (docs/extensions/bots.md "Group access for teammates", #16848): GET / PUT
// /api/v1/bots/feishu/group-access. Clients read the whole policy, change it and write the whole policy back at the
// revision they read; a stale revision answers 409. Pure helpers here; the section is views/settings/integrations/FeishuGroupAccess.tsx.
import { t } from "../i18n";
import { api, ApiError } from "./api";

export type GroupAccessMode = "owner_only" | "allowlist" | "everyone_in_allowed_chats";

export interface GroupUser {
  open_id: string;
  name?: string;
}

export interface GroupChat {
  chat_id: string;
  name?: string;
}

export interface GroupAccessPolicy {
  mode: GroupAccessMode;
  allowed_users: GroupUser[];
  allowed_chats: GroupChat[];
  blocked_users: GroupUser[];
  blocked_chats: GroupChat[];
  guest_tools: string[];
  per_chat_hourly: number;
}

/** A read-only connector tool the owner may give guests; `connector` is the connector's display name. */
export interface GuestToolOption {
  name: string;
  connector: string;
}

/** A recent @bot the policy refused, memory only: whom to allow or block. `at` is unix seconds. */
export interface GroupRequest {
  open_id: string;
  name?: string;
  chat_id: string;
  chat_name?: string;
  at: number;
}

export interface GroupAccessLimits {
  list_max: number;
  guest_tools_max: number;
  per_chat_hourly_min: number;
  per_chat_hourly_max: number;
  per_chat_hourly_default: number;
}

export interface GroupAccess {
  revision: number;
  /** The bot has an owner; an unpaired bot has no group access to show. */
  paired: boolean;
  policy: GroupAccessPolicy;
  available_tools: GuestToolOption[];
  recent_requests: GroupRequest[];
  limits: GroupAccessLimits;
}

/** The questions-per-group-per-hour choices the picker offers; the value in force is always added (hourlyChoices). */
export const HOURLY_CHOICES = [5, 10, 20, 40, 60, 120];

/** Null when the hub predates group access (404): the section is hidden, nothing else changes. */
export async function groupAccess(): Promise<GroupAccess | null> {
  try {
    return await api<GroupAccess>("/bots/feishu/group-access");
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null;
    throw e;
  }
}

export const updateGroupAccess = (revision: number, policy: GroupAccessPolicy) =>
  api<GroupAccess>("/bots/feishu/group-access", { method: "PUT", json: { revision, policy } });

/** The hub says the revision we wrote from is stale: someone changed the policy elsewhere. */
export function isGroupAccessConflict(e: unknown): boolean {
  return e instanceof ApiError && e.status === 409;
}

type Member = GroupUser | GroupChat;

function memberID(m: Member): string {
  return "open_id" in m ? m.open_id : m.chat_id;
}

/** The list without the entry named `id` (an open id or a chat id). */
export function withoutMember<T extends Member>(list: T[], id: string): T[] {
  return list.filter((m) => memberID(m) !== id);
}

/** The list with `entry` added; an entry already there is replaced, so a label can be corrected. */
export function withMember<T extends Member>(list: T[], entry: T): T[] {
  return [...withoutMember(list, memberID(entry)), entry];
}

/** Whether the list names `id`. */
export function hasMember(list: Member[], id: string): boolean {
  return list.some((m) => memberID(m) === id);
}

export function withMode(p: GroupAccessPolicy, mode: GroupAccessMode): GroupAccessPolicy {
  return { ...p, mode };
}

/** guest_tools with `name` added or removed. */
export function withTool(p: GroupAccessPolicy, name: string, on: boolean): GroupAccessPolicy {
  const without = p.guest_tools.filter((n) => n !== name);
  return { ...p, guest_tools: on ? [...without, name] : without };
}

/** The policy's guest tools that are still installed. The hub refuses a whole PUT naming a tool it no longer offers,
 *  so a removed connector's tool is dropped on the next save rather than blocking every change. */
export function usableTools(p: GroupAccessPolicy, available: GuestToolOption[]): GroupAccessPolicy {
  const names = new Set(available.map((o) => o.name));
  return { ...p, guest_tools: p.guest_tools.filter((n) => names.has(n)) };
}

/** The picker's choices in order, with the value in force included even when it is not one of the standard ones. */
export function hourlyChoices(current: number): number[] {
  return [...new Set([...HOURLY_CHOICES, current])].sort((a, b) => a - b);
}

/** One line under the mode picker: what the mode lets people do. */
export function modeDetail(mode: GroupAccessMode): string {
  switch (mode) {
    case "allowlist":
      return t("settings-feishubot.group_access_detail_allowlist");
    case "everyone_in_allowed_chats":
      return t("settings-feishubot.group_access_detail_everyone");
    default:
      return t("settings-feishubot.group_access_detail_owner_only");
  }
}

/** Allowing a recent request: the person goes on the allowed people, and their group on the allowed groups (both
 *  modes that answer guests need the group). Entries already listed keep their labels. */
export function allowRequest(p: GroupAccessPolicy, r: GroupRequest): GroupAccessPolicy {
  const user: GroupUser = r.name ? { open_id: r.open_id, name: r.name } : { open_id: r.open_id };
  const chat: GroupChat = r.chat_name ? { chat_id: r.chat_id, name: r.chat_name } : { chat_id: r.chat_id };
  return {
    ...p,
    allowed_users: hasMember(p.allowed_users, r.open_id) ? p.allowed_users : [...p.allowed_users, user],
    allowed_chats: hasMember(p.allowed_chats, r.chat_id) ? p.allowed_chats : [...p.allowed_chats, chat],
  };
}

/** Blocking a recent request: the person goes on the blocked people, whatever the mode. */
export function blockRequest(p: GroupAccessPolicy, r: GroupRequest): GroupAccessPolicy {
  const user: GroupUser = r.name ? { open_id: r.open_id, name: r.name } : { open_id: r.open_id };
  return { ...p, blocked_users: withMember(p.blocked_users, user) };
}
