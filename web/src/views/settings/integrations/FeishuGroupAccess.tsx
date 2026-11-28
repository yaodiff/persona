// Group access for the built-in Lark bot (#16848): who besides the owner may @ the bot in a group, and what a guest may use.
// Shown in Chat & Delivery under the topic groups, only when the bot is paired and the hub has the route.
import { useCallback, useEffect, useState } from "react";
import { t } from "../../../i18n";
import { errorText } from "../../../lib/api";
import type { FeishuBotStatus } from "../../../lib/feishu-bot";
import {
  allowRequest, blockRequest, groupAccess, GroupAccess, GroupAccessMode, GroupAccessPolicy, GroupRequest, hasMember, hourlyChoices, isGroupAccessConflict,
  modeDetail, updateGroupAccess, usableTools, withMember, withMode, withoutMember, withTool,
} from "../../../lib/feishu-group-access";
import { Button, IconButton, Menu, useMenu } from "../../../ui/atoms";
import { Group, Row, settingsInputClass, Switch } from "../SettingsParts";

type Save = (change: (p: GroupAccessPolicy) => GroupAccessPolicy) => Promise<boolean>;

export function FeishuGroupAccess({ status, saving }: { status: FeishuBotStatus; saving: boolean }) {
  const [access, setAccess] = useState<GroupAccess | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const paired = !!status.owner;
  const pairedAt = status.owner?.paired_at;

  useEffect(() => {
    if (!paired) {
      setAccess(null);
      return;
    }
    let live = true;
    groupAccess().then(
      (next) => {
        if (live) setAccess(next);
      },
      (e) => {
        if (live) setError(t("settings-feishubot.group_access_load_failed", { error: errorText(e) }));
      },
    );
    return () => {
      live = false;
    };
  }, [paired, pairedAt]);

  const reload = useCallback(async () => {
    try {
      setAccess(await groupAccess());
    } catch (e) {
      setError(t("settings-feishubot.group_access_load_failed", { error: errorText(e) }));
    }
  }, []);

  // Every change is read-modify-write of the whole policy at the revision on screen; a stale revision reloads once.
  const save: Save = async (change) => {
    if (!access || busy) return false;
    setBusy(true);
    try {
      const next = await updateGroupAccess(access.revision, usableTools(change(access.policy), access.available_tools));
      setAccess(next);
      setError("");
      return true;
    } catch (e) {
      if (isGroupAccessConflict(e)) {
        setError(t("settings-feishubot.group_access_conflict"));
        await reload();
      } else {
        setError(t("settings-feishubot.group_access_save_failed", { error: errorText(e) }));
      }
      return false;
    } finally {
      setBusy(false);
    }
  };

  if (!access || !access.paired) return null;
  const { policy, limits, available_tools: tools, recent_requests: requests } = access;
  const groupsOn = policy.mode !== "owner_only";
  const peopleOn = policy.mode === "allowlist";
  const off = saving || busy;
  const topicSuggestions = (status.topic_chats ?? []).filter((g) => !hasMember(policy.allowed_chats, g.chat_id));

  return (
    <>
      <Group title={t("settings-feishubot.group_access_title")} footer={t("settings-feishubot.group_access_footer")} anchor="group-access">
        <Row label={t("settings-feishubot.group_access_mode")} hint={modeDetail(policy.mode)} wrapHint stackOnNarrow>
          <select
            aria-label={t("settings-feishubot.group_access_mode")}
            className={settingsInputClass}
            disabled={off}
            value={policy.mode}
            onChange={(e) => void save((p) => withMode(p, e.target.value as GroupAccessMode))}
          >
            <option value="owner_only">{t("settings-feishubot.group_access_mode_owner_only")}</option>
            <option value="allowlist">{t("settings-feishubot.group_access_mode_allowlist")}</option>
            <option value="everyone_in_allowed_chats">{t("settings-feishubot.group_access_mode_everyone")}</option>
          </select>
        </Row>
        {groupsOn && <p className="px-4 py-2.5 text-label leading-snug text-ink-2">{t("settings-feishubot.group_access_guest_caution")}</p>}
        <Row label={t("settings-feishubot.group_access_per_hour")} hint={t("settings-feishubot.group_access_per_hour_hint")} wrapHint stackOnNarrow>
          <select
            aria-label={t("settings-feishubot.group_access_per_hour")}
            className={`${settingsInputClass} w-auto`}
            disabled={off}
            value={policy.per_chat_hourly}
            onChange={(e) => void save((p) => ({ ...p, per_chat_hourly: Number(e.target.value) }))}
          >
            {hourlyChoices(policy.per_chat_hourly).map((n) => (
              <option key={n} value={n}>{n}</option>
            ))}
          </select>
        </Row>
      </Group>

      {groupsOn && (
        <Group title={t("settings-feishubot.group_access_groups")} footer={t("settings-feishubot.group_access_groups_footer")}>
          <MemberRows
            kind="chat"
            rows={policy.allowed_chats.map((c) => ({ id: c.chat_id, name: c.name }))}
            off={off}
            suggestions={topicSuggestions}
            full={policy.allowed_chats.length >= limits.list_max}
            onAdd={(id, name) => save((p) => ({ ...p, allowed_chats: withMember(p.allowed_chats, name ? { chat_id: id, name } : { chat_id: id }) }))}
            onRemove={(id) => save((p) => ({ ...p, allowed_chats: withoutMember(p.allowed_chats, id) }))}
          />
        </Group>
      )}

      {peopleOn && (
        <Group title={t("settings-feishubot.group_access_people")} footer={t("settings-feishubot.group_access_people_footer")}>
          <MemberRows
            kind="user"
            rows={policy.allowed_users.map((u) => ({ id: u.open_id, name: u.name }))}
            off={off}
            full={policy.allowed_users.length >= limits.list_max}
            onAdd={(id, name) => save((p) => ({ ...p, allowed_users: withMember(p.allowed_users, name ? { open_id: id, name } : { open_id: id }) }))}
            onRemove={(id) => save((p) => ({ ...p, allowed_users: withoutMember(p.allowed_users, id) }))}
          />
        </Group>
      )}

      {requests.length > 0 && (
        <Group title={t("settings-feishubot.group_access_requests")} footer={t("settings-feishubot.group_access_requests_footer")}>
          {requests.map((r) => (
            <RequestRow key={`${r.open_id}/${r.chat_id}`} request={r} off={off} onAllow={() => void save((p) => allowRequest(p, r))} onBlock={() => void save((p) => blockRequest(p, r))} />
          ))}
        </Group>
      )}

      <Group title={t("settings-feishubot.group_access_blocked")} footer={t("settings-feishubot.group_access_blocked_footer")}>
        <MemberRows
          kind="user"
          label={t("settings-feishubot.group_access_blocked_people")}
          rows={policy.blocked_users.map((u) => ({ id: u.open_id, name: u.name }))}
          off={off}
          full={policy.blocked_users.length >= limits.list_max}
          onAdd={(id, name) => save((p) => ({ ...p, blocked_users: withMember(p.blocked_users, name ? { open_id: id, name } : { open_id: id }) }))}
          onRemove={(id) => save((p) => ({ ...p, blocked_users: withoutMember(p.blocked_users, id) }))}
        />
        <MemberRows
          kind="chat"
          label={t("settings-feishubot.group_access_blocked_groups")}
          rows={policy.blocked_chats.map((c) => ({ id: c.chat_id, name: c.name }))}
          off={off}
          full={policy.blocked_chats.length >= limits.list_max}
          onAdd={(id, name) => save((p) => ({ ...p, blocked_chats: withMember(p.blocked_chats, name ? { chat_id: id, name } : { chat_id: id }) }))}
          onRemove={(id) => save((p) => ({ ...p, blocked_chats: withoutMember(p.blocked_chats, id) }))}
        />
      </Group>

      {groupsOn && (
        <Group title={t("settings-feishubot.group_access_tools")} footer={t("settings-feishubot.group_access_tools_footer")}>
          {tools.length === 0 && <p className="px-4 py-2.5 text-label text-ink-3">{t("settings-feishubot.group_access_no_tools")}</p>}
          {tools.map((tool) => {
            const on = policy.guest_tools.includes(tool.name);
            const label = t("settings-feishubot.group_access_tool_label", { connector: tool.connector, tool: tool.name });
            return (
              <Row key={tool.name} label={label}>
                <Switch
                  on={on}
                  disabled={off || (!on && policy.guest_tools.length >= limits.guest_tools_max)}
                  label={label}
                  onChange={(next) => void save((p) => withTool(p, tool.name, next))}
                />
              </Row>
            );
          })}
        </Group>
      )}

      {error && <p role="alert" className="mb-6 px-1 text-label break-words text-red">{error}</p>}
    </>
  );
}

type MemberRow = { id: string; name?: string };

/** One list of people or groups: rows to remove, and an "add" form. A group can be picked from the topic groups. */
function MemberRows({
  kind, label, rows, off, full, suggestions = [], onAdd, onRemove,
}: {
  kind: "user" | "chat";
  label?: string;
  rows: MemberRow[];
  off: boolean;
  full: boolean;
  suggestions?: { chat_id: string; name?: string }[];
  onAdd: (id: string, name: string) => Promise<boolean>;
  onRemove: (id: string) => void;
}) {
  const [adding, setAdding] = useState(false);
  const [id, setID] = useState("");
  const [name, setName] = useState("");
  const trimmed = id.trim();
  const valid = trimmed !== "" && !/\s/.test(trimmed) && trimmed.length <= 100 && !rows.some((r) => r.id === trimmed);
  const add = async () => {
    if (!valid) return;
    if (await onAdd(trimmed, name.trim())) {
      setAdding(false);
      setID("");
      setName("");
    }
  };
  const idLabel = kind === "chat" ? t("settings-feishubot.group_access_group_id") : t("settings-feishubot.group_access_person_id");
  return (
    <>
      {label && <div className="px-4 pt-2.5 text-label font-medium text-ink-3">{label}</div>}
      {rows.map((r) => (
        <Row key={r.id} label={r.name || r.id} hint={r.name ? r.id : undefined} mono={!r.name}>
          <IconButton icon="x" label={t("settings-feishubot.group_access_remove", { name: r.name || r.id })} disabled={off} onClick={() => onRemove(r.id)} />
        </Row>
      ))}
      {adding ? (
        <div className="space-y-3 px-4 py-3">
          {kind === "chat" && suggestions.length > 0 && (
            <select aria-label={t("settings-feishubot.group_access_pick_group")} className={settingsInputClass} disabled={off} value=""
              onChange={(e) => {
                const g = suggestions.find((s) => s.chat_id === e.target.value);
                if (g) { setID(g.chat_id); setName(g.name ?? ""); }
              }}>
              <option value="">{t("settings-feishubot.group_access_pick_group")}</option>
              {suggestions.map((g) => <option key={g.chat_id} value={g.chat_id}>{g.name || g.chat_id}</option>)}
            </select>
          )}
          <label className="block text-label text-ink">{idLabel}<input className={settingsInputClass} value={id} aria-label={idLabel} disabled={off} autoComplete="off" spellCheck={false} placeholder={kind === "chat" ? "oc_…" : "ou_…"} onChange={(e) => setID(e.target.value)} /></label>
          <label className="block text-label text-ink">{t("settings-feishubot.group_access_name")}<input className={settingsInputClass} value={name} aria-label={t("settings-feishubot.group_access_name")} disabled={off} onChange={(e) => setName(e.target.value)} /></label>
          <div className="flex flex-wrap justify-end gap-2">
            <Button disabled={off} onClick={() => setAdding(false)}>{t("common.cancel")}</Button>
            <Button tone="accent" busy={off} disabled={off || !valid} onClick={() => void add()}>{t("settings-feishubot.group_access_add")}</Button>
          </div>
        </div>
      ) : (
        <div className="flex justify-end px-4 py-2.5">
          <Button disabled={off || full} onClick={() => setAdding(true)}>
            {t(kind === "chat" ? "settings-feishubot.group_access_add_group" : "settings-feishubot.group_access_add_person")}
          </Button>
        </div>
      )}
    </>
  );
}

/** A recent request, with Allow and Block in one menu so the row keeps a single action. */
function RequestRow({ request, off, onAllow, onBlock }: { request: GroupRequest; off: boolean; onAllow: () => void; onBlock: () => void }) {
  const menu = useMenu();
  const who = request.name || request.open_id;
  return (
    <Row label={who} hint={request.chat_name || request.chat_id} wrapHint>
      <IconButton icon="more" label={t("settings-feishubot.group_access_request_actions", { name: who })} disabled={off} onClick={menu.open} active={!!menu.anchor} />
      {menu.anchor && (
        <Menu
          anchor={menu.anchor}
          onClose={menu.close}
          align="right"
          width={180}
          items={[
            { key: "allow", label: t("settings-feishubot.group_access_allow"), icon: "check", onSelect: onAllow },
            { key: "block", label: t("settings-feishubot.group_access_block"), icon: "x", divider: true, onSelect: onBlock },
          ]}
        />
      )}
    </Row>
  );
}

