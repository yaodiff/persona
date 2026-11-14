import { Suspense, useEffect, useRef } from "react";
import { t } from "../i18n";
import { lazyPage } from "../lib/lazy-page";
import type { Session } from "../lib/types";
import type { WorkspaceTarget } from "../lib/workspace-target";
import { closeWorktreeActions, openWorktreeActions, useWorktreeActionsRequest } from "../lib/worktree-actions";
import type { WorktreeMutation } from "../lib/worktree-actions-state";
import { IconButton, Menu } from "../ui/atoms";
import { OverlayBoundary } from "./LazyOverlay";

const ActiveWorktreeActions = lazyPage(() => import("./WorktreeActionsMenu").then((m) => m.ActiveWorktreeActions));

export function WorktreeActionsButton({ target }: { target: WorkspaceTarget }) {
  return <IconButton icon="more" label={t("worktree-actions.actions")} onClick={(event) => openWorktreeActions(target, event.currentTarget)} />;
}
export interface WorktreeActionsHostProps {
  onOpenWorkspace?: (target: WorkspaceTarget) => void;
  /** The sidebar can supply its complete navigation order, including pinned direct Agents. */
  sessions?: readonly Session[];
  readOnly?: boolean;
}
export type WorktreeMutationRegistry = Map<string, WorktreeMutation>;

/** Keep operation receipts across menu reopening, and load their UI only on the first workspace action. */
export function WorktreeActionsHost(props: WorktreeActionsHostProps) {
  const request = useWorktreeActionsRequest();
  const registry = useRef<WorktreeMutationRegistry>(new Map());
  useEffect(() => {
    const models = registry.current;
    return () => { models.forEach((model) => model.dispose()); models.clear(); };
  }, []);
  if (!request) return null;
  const close = () => closeWorktreeActions(request.id);
  return <OverlayBoundary open onClose={close}>
    <Suspense fallback={<Menu anchor={request.at?.anchor || request.anchor} point={request.at?.point} onClose={close} align="right"
      header={request.target.label || t("worktree-actions.worktree")} headerAsWritten={Boolean(request.target.label)}
      items={[{ key: "loading", label: t("worktree-actions.checking_access"), note: true, onSelect: () => {} }]} />}>
      <ActiveWorktreeActions key={request.id} request={request} registry={registry.current} {...props} />
    </Suspense>
  </OverlayBoundary>;
}
