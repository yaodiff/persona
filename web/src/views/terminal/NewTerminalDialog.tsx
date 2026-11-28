import { useMemo, useState } from "react";
import { t } from "../../i18n";
import { api, enc, errorText } from "../../lib/api";
import { useScopedMachines } from "../../lib/current-assistant";
import { navigate } from "../../lib/router";
import { useStore } from "../../lib/store";
import { usePanes } from "../../lib/terminal-panes";
import { buildTerminalTree, homeShellHost } from "../../lib/terminals";
import { Button, Dialog } from "../../ui/atoms";

/** Same node protocol and workspace/host selection as the terminal inventory's New terminal. */
export function NewTerminalDialog({ open, onClose, onOpened }: { open: boolean; onClose: () => void; onOpened?: () => void }) {
  const machines = useScopedMachines();
  const { listing, busy, refresh } = usePanes(machines, open);
  const newSpace = useStore(s => s.features.includes("pane_new_space"));
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const choices = useMemo(() => machines.filter(m => m.online).map(machine => ({ machine,
    spaces: buildTerminalTree(listing[machine.id]?.data, []).filter(s => s.canOpen),
    home: newSpace ? homeShellHost(listing[machine.id]?.data) : undefined,
  })), [machines, listing, newSpace]);
  const start = async (mid: string, at: { kind: string; workspace_id: string } | { kind: string; cwd: "~"; new_space: true }) => {
    if (pending) return;
    setPending(true); setError("");
    try {
      const result = await api<{ terminal: string }>(`/machines/${enc(mid)}/panes`, { method: "POST", json: at });
      navigate({ name: "terminal", mid, id: result.terminal });
      onClose(); onOpened?.();
    } catch (e) { setError(t("terminal-terminalpage.new_failed", { error: errorText(e) })); }
    finally { setPending(false); }
  };
  return <Dialog open={open} onClose={onClose} title={t("terminal-terminalpage.new_terminal")}>
    <div className="space-y-3">
      {busy && <p role="status" className="text-xs text-ink-3">{t("terminal-terminalpage.loading")}</p>}
      {!choices.length && <p className="text-sm text-ink-2">{t("terminal-terminalpage.none_online")}</p>}
      {choices.map(({ machine, spaces, home }) => <section key={machine.id} className="space-y-2">
        <h2 className="text-sm font-medium text-ink">{machine.name}</h2>
        {listing[machine.id]?.error ? <div role="alert" className="text-xs text-red">{listing[machine.id].error} <Button tone="quiet" onClick={() => void refresh()}>{t("sidebar.retry")}</Button></div> : <div className="flex flex-col items-stretch gap-2">
          {spaces.map(space => <Button key={`${space.host}/${space.id}`} disabled={pending} onClick={() => void start(machine.id, { kind: space.host, workspace_id: space.id })}>
            {t("terminal-terminalpage.new_in_space", { name: space.label })}
          </Button>)}
          {!spaces.length && home && <Button disabled={pending} onClick={() => void start(machine.id, { kind: home, cwd: "~", new_space: true })}>{t("terminal-terminalpage.new_terminal")}</Button>}
          {!busy && listing[machine.id]?.data && !spaces.length && !home && <p className="text-xs text-ink-3">{t("terminal-terminalpage.no_panes_agent")}</p>}
        </div>}
      </section>)}
      {error && <p role="alert" className="text-xs text-red">{error}</p>}
      {pending && <p role="status" className="text-xs text-ink-3">{t("terminal-terminalpage.loading")}</p>}
    </div>
  </Dialog>;
}
