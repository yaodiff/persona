import { useEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, enc, errorText } from "../../lib/api";
import type { MachineTerminalTarget } from "../../lib/machine-terminal";
import { navigate } from "../../lib/router";
import type { Listing } from "../../lib/terminal-panes";
import { terminalId } from "../../lib/terminals";
import type { Machine } from "../../lib/types";
import { sameDirectory } from "../../lib/workspace-directory";
import { Button, Loading } from "../../ui/atoms";

export function MachineTerminalEntry({ target, machine, listing, canCreateSpace, canOpenFolder, retry }: {
  target: MachineTerminalTarget; machine?: Machine; listing?: Listing; canCreateSpace: boolean; canOpenFolder: boolean; retry: () => void;
}) {
  const { mid, cwd, workspaceId, host } = target;
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const mounted = useRef(false);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const hosts = listing?.data?.hosts ?? [];
  const boundHost = hosts.find(h => h.kind === host);
  // Runtime workspaces can live outside the terminal host. Their folder is the terminal destination.
  const scoped = boundHost ? [boundHost] : hosts;
  const matches = (id: string, path?: string) => boundHost && workspaceId ? id === workspaceId : !cwd || sameDirectory(path || "", cwd);
  const workspaces = scoped.flatMap(h => (h.workspaces ?? []).filter(w => matches(w.id, w.cwd)).map(w => ({ h, w })));
  const workspace = workspaces.find(({ h }) => h.new_tab);
  // Match the Terminals inventory: a live Agent owns its pane; a standalone shell can be resumed here.
  const existing = scoped.filter(h => h.stream).flatMap(h => (h.panes ?? []).filter(p =>
    !p.session_id && !p.agent && (matches(p.workspace_id, p.cwd) ||
      workspaces.some(({ h: owner, w }) => owner === h && w.id === p.workspace_id)))).at(0);
  const id = existing && terminalId(existing);
  const ready = machine?.online && !listing?.error;
  useEffect(() => {
    if (ready && id && !pending) navigate({ name: "terminal", mid, id, view: "terminals" }, true, {});
  }, [ready, id, mid, pending]);

  const kind = (scoped.find(h => h.new_tab) ?? hosts.find(h => h.new_tab))?.kind;
  const at = workspace ? { kind: workspace.h.kind, workspace_id: workspace.w.id }
    : canOpenFolder && cwd && kind ? { kind, cwd }
    : canCreateSpace && kind ? { kind, cwd: cwd || "~", new_space: true } : undefined;
  const start = async () => {
    if (!at || pending || !ready) return;
    setPending(true);
    setError("");
    try {
      const result = await api<{ terminal: string }>(`/machines/${enc(mid)}/panes`, { method: "POST", json: at });
      if (mounted.current) navigate({ name: "terminal", mid, id: result.terminal, view: "terminals" }, true, {});
    } catch (e) {
      setError(t("terminal-terminalpage.new_failed", { error: errorText(e) }));
    } finally {
      setPending(false);
    }
  };
  return <section className="mx-auto w-full max-w-xl space-y-3 p-4">
    <Button icon="arrowLeft" tone="quiet" onClick={() => navigate({ name: "terminal" }, true, {})}>{t("work-entry.back")}</Button>
    <div className="space-y-3 rounded-card bg-surface p-4 shadow-card">
      <h1 className="text-base font-semibold">{t("terminal-terminalpage.terminals")}</h1>
      <p className="font-medium text-ink">{machine?.name ?? mid}</p>
      {cwd && <p className="break-all text-sm text-ink-2">{cwd}</p>}
      {!machine?.online ? <p>{t("terminal-terminalpage.offline")}</p>
        : listing?.error ? <div role="alert">{listing.error} <Button onClick={retry}>{t("terminal-terminalpage.retry")}</Button></div>
        : !listing?.data || id ? <Loading />
        : at ? <Button tone="primary" icon="plus" busy={pending} onClick={() => void start()}>{t("terminal-terminalpage.new_terminal")}</Button>
        : <p>{t("work-entry.terminal_unavailable")}</p>}
      {error && <p role="alert" className="text-sm text-red">{error}</p>}
    </div>
  </section>;
}
