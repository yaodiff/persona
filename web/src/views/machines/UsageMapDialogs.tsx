// The disk usage map's confirmations (#14576, docs/server/machine-usage-map.md "Confirmations"): every change names
// what it touches before it runs. Trash lists each item, its path and the total, and says the space returns only when
// the trash is emptied; Delete Permanently is its own danger step. Offload names the provider, the items and the total;
// a missing tool brings a second confirmation naming the tool, the computer and where the tool comes from. Clean cache
// is the alert sheet's own wording (AlertDisk.tsx) over the existing cleanup call.
import { t } from "../../i18n";
import { bytesBin } from "../../lib/machine-detail";
import type { OffloadProvider, OffloadTool, UsageNode } from "../../lib/usage-map";
import { Button, Dialog } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Segmented } from "../assistant/parts";
import { cacheTitle } from "./LegacyCacheRecovery";

export const sumBytes = (nodes: readonly UsageNode[]) => nodes.reduce((s, n) => s + Math.max(0, n.bytes), 0);

function ItemList({ nodes, name, testId }: { nodes: readonly UsageNode[]; name: (n: UsageNode) => string; testId: string }) {
  return (
    <ul className="mt-2 max-h-56 space-y-1 overflow-y-auto" data-testid={testId}>
      {nodes.map((n) => (
        <li key={n.id} className="text-meta" data-item={n.path ?? n.id}>
          <div className="flex items-baseline gap-2">
            <span className="min-w-0 flex-1 truncate text-ink">{name(n)}</span>
            <span className="shrink-0 text-ink-2 tabular-nums">{bytesBin(n.bytes)}</span>
          </div>
          {(n.display ?? n.path ?? n.cloud?.origin_path) && <div className="truncate font-mono text-submeta text-ink-3">{n.display ?? n.path ?? n.cloud?.origin_path}</div>}
        </li>
      ))}
    </ul>
  );
}

function Failure({ text }: { text: string }) {
  if (!text) return null;
  return (
    <p role="alert" className="mt-2 flex items-start gap-2 rounded-control bg-red-tint px-3 py-2 text-meta text-red">
      <Icon name="warning" size={14} className="mt-0.5 shrink-0" />
      <span>{text}</span>
    </p>
  );
}

function Footer({ busy, onCancel, confirm, tone = "danger", onConfirm, testId, disabled }: { busy: boolean; onCancel: () => void; confirm: string; tone?: "danger" | "primary"; onConfirm: () => void; testId: string; disabled?: boolean }) {
  return (
    <>
      <Button tone="quiet" disabled={busy} onClick={onCancel}>
        {t("common.cancel")}
      </Button>
      <Button tone={tone} busy={busy} disabled={disabled} onClick={onConfirm} testId={testId}>
        {confirm}
      </Button>
    </>
  );
}

interface Common {
  machine: string;
  busy: boolean;
  error: string;
  onCancel: () => void;
  onConfirm: () => void;
  name: (n: UsageNode) => string;
}

export function TrashDialog({ nodes, ...c }: Common & { nodes: readonly UsageNode[] }) {
  const total = sumBytes(nodes);
  return (
    <Dialog
      open
      onClose={() => !c.busy && c.onCancel()}
      title={t("machines-usagemap.trash_confirm_title", { count: nodes.length })}
      footer={<Footer busy={c.busy} onCancel={c.onCancel} confirm={t("machines-usagemap.trash_confirm")} onConfirm={c.onConfirm} testId="usage-trash-confirm" />}
    >
      <p className="text-meta leading-relaxed text-ink-2">{t("machines-usagemap.trash_confirm_body", { machine: c.machine, size: bytesBin(total) })}</p>
      <ItemList nodes={nodes} name={c.name} testId="usage-trash-list" />
      <p className="mt-2 text-submeta text-ink-3">{t("machines-usagemap.trash_confirm_note", { size: bytesBin(total) })}</p>
      <Failure text={c.error} />
    </Dialog>
  );
}

export function PurgeDialog({ count, bytes, ...c }: Omit<Common, "name"> & { count: number; bytes: number }) {
  return (
    <Dialog
      open
      onClose={() => !c.busy && c.onCancel()}
      title={t("machines-usagemap.trash_purge_title")}
      footer={<Footer busy={c.busy} onCancel={c.onCancel} confirm={t("machines-usagemap.trash_purge_confirm")} onConfirm={c.onConfirm} testId="usage-purge-confirm" />}
    >
      <p className="text-meta leading-relaxed text-ink-2">{t("machines-usagemap.trash_purge_body", { count, machine: c.machine, size: bytesBin(bytes) })}</p>
      <Failure text={c.error} />
    </Dialog>
  );
}

export function OffloadDialog({
  nodes,
  providers,
  provider,
  onProvider,
  refused,
  ...c
}: Common & { nodes: readonly UsageNode[]; providers: readonly OffloadProvider[]; provider: OffloadProvider; onProvider: (p: OffloadProvider) => void; refused: { path?: string; reason: string }[] }) {
  const total = sumBytes(nodes);
  const key = (p: OffloadProvider) => `${p.extension}/${p.id}`;
  return (
    <Dialog
      open
      onClose={() => !c.busy && c.onCancel()}
      title={t("machines-usagemap.offload_confirm_title", { provider: provider.name })}
      footer={<Footer busy={c.busy} tone="primary" onCancel={c.onCancel} confirm={t("machines-usagemap.offload_confirm")} onConfirm={c.onConfirm} testId="usage-offload-confirm" />}
    >
      {providers.length > 1 && (
        <div className="mb-2">
          <Segmented value={key(provider)} onChange={(v) => onProvider(providers.find((p) => key(p) === v) ?? provider)} label={t("machines-usagemap.offload_provider")} options={providers.map((p) => ({ v: key(p), label: p.name }))} />
        </div>
      )}
      <p className="text-meta leading-relaxed text-ink-2">{t("machines-usagemap.offload_confirm_body", { count: nodes.length, size: bytesBin(total), machine: c.machine, provider: provider.name })}</p>
      <ItemList nodes={nodes} name={c.name} testId="usage-offload-list" />
      <p className="mt-2 text-submeta text-ink-3">{provider.restore ? t("machines-usagemap.offload_confirm_restore_note") : t("machines-usagemap.offload_confirm_no_restore_note")}</p>
      {refused.length > 0 && (
        <div role="alert" className="mt-2 rounded-control bg-field px-3 py-2 text-meta" data-testid="usage-offload-refused">
          <p className="font-medium text-ink">{t("machines-usagemap.offload_refused", { provider: provider.name })}</p>
          <ul className="mt-1 space-y-0.5">
            {refused.map((r, i) => (
              <li key={r.path ?? i} className="text-submeta text-ink-2">
                <span className="font-mono">{r.path}</span> · {r.reason}
              </li>
            ))}
          </ul>
        </div>
      )}
      <Failure text={c.error} />
    </Dialog>
  );
}

export function InstallDialog({ tool, provider, action, ...c }: Omit<Common, "name"> & { tool: OffloadTool; provider: OffloadProvider; action: "offload" | "restore" }) {
  const name = tool.title || tool.name;
  return (
    <Dialog
      open
      onClose={() => !c.busy && c.onCancel()}
      title={t("machines-usagemap.offload_install_confirm_title", { tool: name })}
      footer={
        <Footer
          busy={c.busy}
          tone="primary"
          onCancel={c.onCancel}
          confirm={action === "restore" ? t("machines-usagemap.offload_install_confirm_restore") : t("machines-usagemap.offload_install_confirm_offload")}
          onConfirm={c.onConfirm}
          testId="usage-install-confirm"
        />
      }
    >
      <div data-testid="usage-install">
        <p className="text-meta leading-relaxed text-ink-2">{t("machines-usagemap.offload_install_confirm_body", { provider: provider.name, tool: name, machine: c.machine })}</p>
        {tool.source && <p className="mt-2 rounded-control bg-field px-3 py-2 text-meta text-ink-2" data-testid="usage-install-source">{tool.source}</p>}
        <p className="mt-2 text-submeta text-ink-3">{t("machines-usagemap.offload_install_confirm_note")}</p>
        <Failure text={c.error} />
      </div>
    </Dialog>
  );
}

export function RestoreDialog({ nodes, provider, ...c }: Common & { nodes: readonly UsageNode[]; provider: OffloadProvider }) {
  const total = sumBytes(nodes);
  return (
    <Dialog
      open
      onClose={() => !c.busy && c.onCancel()}
      title={t("machines-usagemap.offload_restore_title", { provider: provider.name })}
      footer={<Footer busy={c.busy} tone="primary" onCancel={c.onCancel} confirm={t("machines-usagemap.offload_restore_confirm")} onConfirm={c.onConfirm} testId="usage-restore-confirm" />}
    >
      <p className="text-meta leading-relaxed text-ink-2">{t("machines-usagemap.offload_restore_body", { count: nodes.length, size: bytesBin(total), machine: c.machine })}</p>
      <ItemList nodes={nodes} name={c.name} testId="usage-restore-list" />
      <Failure text={c.error} />
    </Dialog>
  );
}

export function CleanDialog({ nodes, ...c }: Omit<Common, "name"> & { nodes: readonly UsageNode[] }) {
  const total = sumBytes(nodes);
  return (
    <Dialog
      open
      onClose={() => !c.busy && c.onCancel()}
      title={t("machines-alerts.clean_confirm_title")}
      footer={<Footer busy={c.busy} onCancel={c.onCancel} confirm={t("machines-alerts.clean_confirm", { size: bytesBin(total) })} onConfirm={c.onConfirm} testId="usage-clean-confirm" />}
    >
      <p className="text-meta leading-relaxed text-ink-2">{t("machines-alerts.clean_confirm_body", { machine: c.machine, size: bytesBin(total) })}</p>
      <ItemList nodes={nodes} name={(n) => cacheTitle(n.cache_id ?? n.name)} testId="usage-clean-list" />
      <p className="mt-2 text-submeta text-ink-3">{t("machines-alerts.clean_confirm_safe")}</p>
      <Failure text={c.error} />
    </Dialog>
  );
}
