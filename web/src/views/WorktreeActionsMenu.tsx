import { useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { t } from "../i18n";
import { byAttention } from "../lib/format";
import { agentSort, byUpdated } from "../lib/agent-sort";
import { storedValue } from "../lib/compatibility";
import { NO_PINS, isPinned, pinnedSessions } from "../lib/pins";
import { api, credentialOwner } from "../lib/api";
import { MANAGER_HEADER } from "../lib/manager-choice";
import { openNewAgent } from "../lib/new-agent";
import { navigate } from "../lib/router";
import { getState, useStore } from "../lib/store";
import { refreshWorkspaceCatalog, useWorkspaceCatalog } from "../lib/workspace-catalog";
import { closeWorktreeActions, worktreeActionsCurrent, type WorktreeActionsRequest } from "../lib/worktree-actions";
import { firstDirectWorktreeAgent, resolveWorktreeAction, worktreeActionKey, worktreeSafetyStamp } from "../lib/worktree-actions-context";
import { WorktreeMutation } from "../lib/worktree-actions-state";
import type { WorktreeStatus } from "../lib/worktree";
import { copyText, Menu, toast } from "../ui/atoms";
import type { MenuItem } from "../ui/menu-model";
import type { WorktreeActionsHostProps, WorktreeMutationRegistry } from "./WorktreeActions";
import { DeleteWorktreeDialog, RenameWorktreeDialog } from "./WorktreeActionDialogs";

export function ActiveWorktreeActions({ request, registry, onOpenWorkspace, sessions, readOnly = false }: WorktreeActionsHostProps & { request: WorktreeActionsRequest; registry: WorktreeMutationRegistry }) {
  const { target, id, anchor } = request;
  const catalog = useWorkspaceCatalog(true);
  const machines = useStore((state) => state.machines);
  const pins = useStore((state) => state.pins || NO_PINS);
  const storeSessions = useStore((state) => [state.sessions, state.sessionSummaries] as const);
  const [view, setView] = useState<"menu" | "rename" | "delete" | "close">("menu");
  const [linkedWorktree, setLinkedWorktree] = useState<boolean | null>();
  const [access, setAccess] = useState<"loading" | "full" | "readOnly">("loading");
  const key = worktreeActionKey(target);
  const permission = useRef(false);
  permission.current = access === "full" && !readOnly;
  const live = useRef({ catalog, readOnly, view, linkedWorktree });
  live.current = { catalog, readOnly, view, linkedWorktree };
  const current = () => worktreeActionsCurrent(id) && target.owner === credentialOwner();
  const resolve = () => resolveWorktreeAction(target, { catalog: live.current.catalog, machines: getState().machines, writable: permission.current, linkedWorktree: live.current.linkedWorktree === true });
  const context = resolveWorktreeAction(target, { catalog, machines, writable: permission.current, linkedWorktree: linkedWorktree === true });
  const close = () => {
    if (!current()) return;
    closeWorktreeActions(id);
    // Menu -> dialog handoffs restore the original row, not a menu item that has just unmounted.
    if (anchor.isConnected) anchor.focus({ preventScroll: true });
  };
  const [model] = useState(() => {
    const prior = registry.get(key);
    if (prior && !["invalid", "removed"].includes(prior.snapshot().phase)) return prior;
    prior?.dispose();
    const created = new WorktreeMutation(target, { resolve, current, acknowledged: () => {} });
    registry.set(key, created);
    return created;
  });
  const stamp = useRef(context.workspace ? worktreeSafetyStamp(context.workspace) : "");
  model.updateDependencies({ resolve, current, acknowledged: (action) => {
    if (!current()) return;
    void refreshWorkspaceCatalog();
    toast(t(action === "rename" ? "worktree-actions.renamed" : action === "close" ? "worktree-actions.closed" : "worktree-actions.removed"), "ok");
    close();
  } });
  const state = useSyncExternalStore(model.subscribe, model.snapshot, model.snapshot);

  useEffect(() => {
    const controller = new AbortController();
    // This route is full-device-only on existing hubs. No role is guessed from a token's spelling or saved device id.
    void api<{ tokens: { id: string; role?: string }[]; current: string }>("/auth/tokens", {
      signal: controller.signal, headers: { [MANAGER_HEADER]: target.manager },
    }).then((result) => {
      if (!current() || controller.signal.aborted) return;
      const full = Array.isArray(result.tokens) && typeof result.current === "string"
        && (result.current === "" || result.tokens.some((token) => token.id === result.current && token.role === "full"));
      setAccess(full ? "full" : "readOnly");
    }).catch(() => { if (current() && !controller.signal.aborted) setAccess("readOnly"); });
    return () => controller.abort();
  }, [id, target.manager]); // request identity is immutable

  useEffect(() => {
    if (target.host !== "herdr" || access !== "full") return;
    const controller = new AbortController();
    void api<WorktreeStatus>(`/machines/${encodeURIComponent(target.machineId)}/worktree/status`, {
      method: "POST", json: { kind: target.host, cwd: target.cwd }, signal: controller.signal,
      headers: { [MANAGER_HEADER]: target.manager },
    }).then((status) => {
      if (current() && !controller.signal.aborted) setLinkedWorktree(status.worktree && status.path === target.cwd);
    }).catch(() => { if (current() && !controller.signal.aborted) setLinkedWorktree(null); });
    return () => controller.abort();
  }, [id, target.host, access]);

  useLayoutEffect(() => {
    if (access === "loading") return;
    const next = context.workspace ? worktreeSafetyStamp(context.workspace) : "";
    if (!stamp.current && next) stamp.current = next;
    const retainedRemoval = state.dispatched && context.canReconcile && !context.workspace;
    if ((!context.writable && !retainedRemoval) || (stamp.current && next !== stamp.current && !retainedRemoval)) model.invalidate();
  }, [model, access, context.writable, context.workspace, context.canReconcile, state.dispatched]);
  useEffect(() => {
    registry.set(key, model);
    model.activate();
    return () => {
      model.cancel();
      if (!model.snapshot().dispatched) { model.dispose(); registry.delete(key); }
    };
  }, [model, registry, key]);

  const ordered = useMemo(() => {
    if (sessions) return sessions;
    const rows = [...Object.values(storeSessions[0]).flat(), ...storeSessions[1]];
    const seen = new Set<string>();
    const unique = rows.filter((session) => {
      const key = JSON.stringify([session.manager, session.machine_id, session.host, session.session_id]);
      if (seen.has(key)) return false;
      seen.add(key); return true;
    });
    const compare = agentSort(storedValue("persona.agentSort")) === "updated" ? byUpdated : byAttention;
    return [...pinnedSessions(unique, pins), ...unique.filter((session) => !isPinned(session, pins)).sort(compare)];
  }, [sessions, storeSessions, pins]);
  const agent = firstDirectWorktreeAgent(target, catalog, ordered);
  const changeView = (next: "rename" | "delete" | "close") => {
    if (!current()) return;
    anchor.focus({ preventScroll: true });
    setView(next);
  };
  const run = (action: () => void, writing = false) => {
    if (!current()) return;
    const checked = resolve();
    if (!checked.workspace || (writing && !checked.writable)) return;
    close(); action();
  };
  const items: MenuItem[] = [
    { key: "open", label: t("worktree-actions.open"), icon: "folder", disabled: !context.workspace || (!agent && !context.writable), onSelect: () => run(() => {
      if (agent) navigate({ name: "session", mid: agent.machine_id, sid: agent.session_id });
      else openNewAgent({ workspace: target });
    }, !agent) },
    { key: "new-agent", label: t("worktree-actions.new_agent"), icon: "agent", disabled: !context.writable, onSelect: () => run(() => openNewAgent({ workspace: target }), true) },
    ...(context.canCreate ? [{ key: "child", label: t("worktree-actions.new_child"), icon: "git" as const, inline: true, onSelect: async () => {
      // Recheck the originating target after loading the child composer.
      const { openChildWorktree } = await import("../lib/worktree-creation");
      if (!current() || !resolve().canCreate) return;
      close();
      openChildWorktree(target, { changed: () => {
        if (target.owner === credentialOwner() && resolve().writable) void refreshWorkspaceCatalog();
      } });
    } }] : []),
    ...(context.canRename && !state.dispatched ? [{ key: "rename", label: t("worktree-actions.rename"), icon: "pencil" as const, keepOpen: true, onSelect: () => changeView("rename") }] : []),
    { key: "copy-name", label: t("worktree-actions.copy_name"), icon: "copy", divider: true, inline: true, disabled: !context.workspace, onSelect: async () => {
      if (current()) await copyText(resolve().workspace?.title || target.label || target.cwd);
    } },
    { key: "copy-path", label: t("worktree-actions.copy_path"), icon: "copy", inline: true, disabled: !context.workspace || !target.cwd, onSelect: async () => { if (current()) await copyText(target.cwd); } },
    ...(context.parent && onOpenWorkspace ? [{ key: "parent", label: t("worktree-actions.open_parent"), icon: "folder" as const, onSelect: () => {
      const parent = resolve().parent;
      if (current() && parent) { close(); onOpenWorkspace(parent); }
    } }] : []),
    ...(target.host === "herdr" && (!state.dispatched || state.preview?.close_only) ? [{ key: "close", label: t(state.dispatched ? "worktree-actions.check_status" : "worktree-actions.close_workspace"), icon: "x" as const,
      divider: true, danger: true, keepOpen: true, disabled: state.dispatched ? !context.canReconcile : !context.canClose, onSelect: () => changeView("close") }] : []),
    ...(["orca", "herdr", "paseo"].includes(target.host) && !(state.dispatched && state.preview?.close_only) ? [{ key: "delete", label: t(state.dispatched ? "worktree-actions.check_status" : "worktree-actions.delete"), icon: "trash" as const,
      divider: true, danger: true, keepOpen: true, disabled: state.dispatched ? !context.canReconcile : !context.canDelete, onSelect: () => changeView("delete") }] : []),
    ...(target.host === "herdr" && linkedWorktree === false ? [{ key: "linked-only", label: t("worktree-actions.linked_only"), note: true, onSelect: () => {} }] : []),
    ...(target.host === "herdr" && linkedWorktree === null ? [{ key: "git-unavailable", label: t("worktree-actions.unavailable"), note: true, onSelect: () => {} }] : []),
    ...(!context.writable ? [{ key: "availability", label: access === "loading" ? t("worktree-actions.checking_access") : context.reason, note: true, onSelect: () => {} }] : []),
    ...(context.workspace?.is_main || context.hasChildren ? [{ key: "protected", label: t(context.hasChildren ? "worktree-actions.has_children" : "worktree-actions.protected"), note: true, onSelect: () => {} }] : []),
  ];
  return <>
    {view === "menu" && <Menu anchor={request.at?.anchor || anchor} point={request.at?.point} onClose={close} align="right"
      header={context.workspace?.title || target.label || t("worktree-actions.worktree")} headerAsWritten={Boolean(context.workspace?.title || target.label)} items={items} />}
    {view === "rename" && <RenameWorktreeDialog target={target} name={context.workspace?.title || target.label || ""} model={model} onClose={close} />}
    {(view === "delete" || view === "close") && <DeleteWorktreeDialog target={target} machineName={context.machine?.name} closeOnly={view === "close"} model={model} onClose={close} />}
  </>;
}
