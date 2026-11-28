// Assistant composer's @ / + panel wrapper (#12531): loads assistant-only mention sources
// (hub workspaces and bound repositories) on demand when the panel opens, keeping them off
// both the first-screen and idle tiers.
import { useEffect, useMemo, useRef, useState, useSyncExternalStore, type ComponentProps } from "react";
import { api, credentialOwner, subscribeCredentialOwner } from "../../lib/api";
import { workspaceContext, workspaceDetail, workspaceMatches, workspaceTitle } from "../../lib/mentions-panel";
import { loadMentionRepositories, repositoryMentionRows, type MentionRepository } from "../../lib/mentions-repositories";
import { machineWithHub } from "../../lib/mesh";
import { useStore } from "../../lib/store";
import type { Machine } from "../../lib/types";
import type { Workspace, WorkspaceList } from "../../lib/workspace-board";
import { ComposerPanel, type AssistantMentions, type MentionRow } from "./ComposerPanel";

type PanelProps = Omit<ComponentProps<typeof ComposerPanel>, "assistant">;
const NO_MACHINES: Machine[] = [];

export interface AssistantComposerPanelProps extends PanelProps {
  /** Controlled machines list used to label workspace computers; skips the global store when supplied. */
  machines?: Machine[];
  /** Controlled machine display-name resolver; overrides `machines` and skips the global store when supplied. */
  machineName?: (machineId: string) => string;
  /** Controlled hub workspaces (`null` while loading, `[]` when empty); skips fetching when supplied. */
  workspaces?: Workspace[] | null;
  /** Controlled workspace loader replacing `GET /workspaces` when `workspaces` is omitted. */
  loadWorkspaces?: (signal: AbortSignal) => Promise<Workspace[]>;
  /** Controlled bound repositories (`null` while loading, `[]` when empty); skips fetching and credential-owner subscription when supplied. */
  repositories?: MentionRepository[] | null;
  /** Controlled repository loader replacing `loadMentionRepositories` when `repositories` is omitted. */
  loadRepositories?: (manager: string | undefined, signal: AbortSignal) => Promise<MentionRepository[]>;
  /** Controlled credential-owner scope key for repository fetches; skips the global credential-owner subscription when supplied. */
  credentialOwner?: string;
}

/** The hub's workspaces, read once each time the panel opens (null while reading; empty when unavailable). */
function useWorkspaces(
  on: boolean,
  controlled: Workspace[] | null | undefined,
  loader: ((signal: AbortSignal) => Promise<Workspace[]>) | undefined,
): Workspace[] | null {
  const isControlled = controlled !== undefined;
  const loaderRef = useRef(loader);
  useEffect(() => {
    loaderRef.current = loader;
  }, [loader]);
  const hasLoader = Boolean(loader);
  const [list, setList] = useState<Workspace[] | null>(null);
  useEffect(() => {
    if (!on || isControlled) return;
    const ac = new AbortController();
    let req: Promise<Workspace[]>;
    try {
      req = loaderRef.current
        ? Promise.resolve(loaderRef.current(ac.signal))
        : api<WorkspaceList>("/workspaces", { signal: ac.signal }).then((l) => l.workspaces ?? []);
    } catch {
      setList((cur) => cur ?? []);
      return () => ac.abort();
    }
    req
      .then((l) => {
        if (!ac.signal.aborted) setList(l ?? []);
      })
      .catch(() => {
        if (!ac.signal.aborted) setList((cur) => cur ?? []);
      });
    return () => ac.abort();
  }, [on, isControlled, hasLoader]);
  return isControlled ? controlled : list;
}

/** The assistant's bound repositories, read once per panel opening (null while reading; empty when unavailable). */
function useRepositories(
  repositories: string | boolean | undefined,
  owner: string,
  controlled: MentionRepository[] | null | undefined,
  loader: ((manager: string | undefined, signal: AbortSignal) => Promise<MentionRepository[]>) | undefined,
): MentionRepository[] | null {
  const isControlled = controlled !== undefined;
  const enabled = Boolean(repositories);
  const manager = typeof repositories === "string" && repositories ? repositories : undefined;
  const loaderRef = useRef(loader);
  useEffect(() => {
    loaderRef.current = loader;
  }, [loader]);
  const hasLoader = Boolean(loader);
  const key = enabled && !isControlled ? `${owner}\n${manager ?? ""}` : "";
  const [state, setState] = useState<{ key: string; list: MentionRepository[] } | null>(null);
  useEffect(() => {
    if (!enabled || isControlled) return;
    const ac = new AbortController();
    let req: Promise<MentionRepository[]>;
    try {
      req = loaderRef.current
        ? Promise.resolve(loaderRef.current(manager, ac.signal))
        : loadMentionRepositories(manager, ac.signal);
    } catch {
      setState((cur) => (cur?.key === key ? cur : { key, list: [] }));
      return () => ac.abort();
    }
    req
      .then((list) => {
        if (!ac.signal.aborted) setState({ key, list: list ?? [] });
      })
      .catch(() => {
        if (!ac.signal.aborted) setState((cur) => (cur?.key === key ? cur : { key, list: [] }));
      });
    return () => ac.abort();
  }, [enabled, isControlled, hasLoader, key, manager]);
  if (!enabled) return null;
  if (isControlled) return controlled;
  return state?.key === key ? state.list : null;
}

function AssistantComposerPanelInner({
  machines = NO_MACHINES,
  machineName,
  workspaces: controlledWorkspaces,
  loadWorkspaces,
  repositories: controlledRepositories,
  loadRepositories,
  credentialOwner: owner = "",
  ...panelProps
}: AssistantComposerPanelProps) {
  const wantWorkspaces = Boolean(panelProps.source?.workspaces);
  const wantRepositories = Boolean(panelProps.source?.repositories);
  const workspaces = useWorkspaces(wantWorkspaces, controlledWorkspaces, loadWorkspaces);
  const repositories = useRepositories(panelProps.source?.repositories, owner, controlledRepositories, loadRepositories);

  const assistant = useMemo<AssistantMentions>(() => ({
    rows: (query: string) => {
      const out: MentionRow[] = [];
      if (wantWorkspaces) {
        const q = query.toLowerCase();
        const name = machineName ?? ((id: string) => machineWithHub(id, machines));
        for (const w of workspaceMatches(workspaces ?? [], q, name)) {
          out.push({ kind: "workspace", id: `workspace:${w.id}`, name: workspaceTitle(w), detail: workspaceDetail(w, name), workspace: w, toContext: () => workspaceContext(w, name) });
        }
      }
      if (wantRepositories) {
        out.push(...repositoryMentionRows(repositories ?? [], query));
      }
      return out;
    },
    browsingRows: wantRepositories ? (query: string) => repositoryMentionRows(repositories ?? [], query) : undefined,
    loading: Boolean((wantWorkspaces && workspaces === null) || (wantRepositories && repositories === null)),
  }), [wantWorkspaces, wantRepositories, machines, machineName, workspaces, repositories]);

  return <ComposerPanel {...panelProps} assistant={assistant} />;
}

function DefaultCredentialOwnerBridge(props: AssistantComposerPanelProps) {
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, () => "");
  return <AssistantComposerPanelInner {...props} credentialOwner={owner} />;
}

function AssistantComposerPanelWithOwner(props: AssistantComposerPanelProps) {
  if (props.source?.repositories && props.repositories === undefined && props.credentialOwner === undefined && !props.loadRepositories) {
    return <DefaultCredentialOwnerBridge {...props} />;
  }
  return <AssistantComposerPanelInner {...props} />;
}

function StoreMachinesBridge(props: AssistantComposerPanelProps) {
  const machines = useStore((s) => s.machines);
  return <AssistantComposerPanelWithOwner {...props} machines={machines} />;
}

export function AssistantComposerPanel(props: AssistantComposerPanelProps) {
  if (props.source?.workspaces && props.machines === undefined && !props.machineName) {
    return <StoreMachinesBridge {...props} />;
  }
  return <AssistantComposerPanelWithOwner {...props} />;
}
