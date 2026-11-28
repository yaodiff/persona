// A Persona chat's model panel on a hub that declares the four dimensions (docs/web/web.md "Model pill and model
// panel"): only the model and the thinking effort, from the combinations the hub recommends on the chat's computer
// (GET /execution/catalog?machine=, not blocked and recommended, one per model + effort). The engine and the channel are
// matched by the hub and folded into "Advanced / route", where all four narrow one another and each combination is
// checked (POST /execution/validate). "All models and providers" opens the launcher panel it had before; a kept chat
// can go back to the last setup that worked (…/model/last-good, …/model/restore).
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import {
  aliasSpec, choosePick, cleanSpec, conversationLastGood, modelLabel, pickEfforts, pickModels, pickOfSpec, restoreConversationLastGood,
  sameSpec, verdictBlocks, type ExecutionCatalog, type ExecutionSpec, type LastGoodSpec, type RecommendedPick, type SpecPick,
} from "../../lib/execution";
import { effortTitle } from "../../lib/session-actions";
import type { Session } from "../../lib/types";
import { Button, Dialog } from "../../ui/atoms";
import { useVerdict } from "./hooks";
import { RouteFields, routeInputClass } from "./RouteFields";
import { specText, VerdictNote } from "./VerdictNote";

export interface SimpleModelSheetProps {
  catalog: ExecutionCatalog;
  picks: RecommendedPick[];
  machine: string;
  /** What the chat runs on now: its launcher, model and effort. */
  current: { launcher: string; model?: string; effort?: string };
  /** The chat whose pick the hub keeps (last-good and restore are its); absent: a new conversation or an older hub. */
  kept?: Session;
  header?: ReactNode;
  /** Applies a pick (the panel's own path: kept on the hub, or this page's draft); false keeps the sheet open. */
  onApply: (pick: SpecPick) => Promise<boolean>;
  /** The chat's pick came back from last-good: the panel draws it. */
  onRestored?: (pick: SpecPick) => void;
  onNotice: (text: string, tone?: "info" | "ok" | "error") => void;
  onMore: () => void;
  onClose: () => void;
}

export function SimpleModelSheet({ catalog, picks, machine, current, kept, header, onApply, onRestored, onNotice, onMore, onClose }: SimpleModelSheetProps) {
  const now = aliasSpec(catalog, current.launcher);
  const startSpec: ExecutionSpec = cleanSpec({ ...now, model: current.model || now?.model, effort: current.effort || now?.effort });
  const [model, setModel] = useState(startSpec.model ?? picks[0]?.model ?? "");
  const [effort, setEffort] = useState(startSpec.effort ?? "");
  const [advanced, setAdvanced] = useState(false);
  const [route, setRoute] = useState<ExecutionSpec>(startSpec);
  const [busy, setBusy] = useState(false);
  const [lastGood, setLastGood] = useState<LastGoodSpec | null>(null);

  useEffect(() => {
    if (!kept) return;
    let live = true;
    conversationLastGood(kept).then((g) => live && setLastGood(g)).catch(() => {});
    return () => {
      live = false;
    };
  }, [kept]);

  const models = pickModels(picks);
  if (model && !models.includes(model)) models.unshift(model);
  const efforts = pickEfforts(picks, model);
  if (effort && !efforts.includes(effort)) efforts.unshift(effort);
  const chosen = choosePick(picks, model, effort);
  // the simple choice runs on its recommended combination (its verdict is the catalog's); the advanced one is checked
  const spec: ExecutionSpec = advanced ? route : chosen ? { ...chosen.spec, model, ...(effort ? { effort } : {}) } : cleanSpec({ model, effort });
  const known = !advanced && chosen && (chosen.effort === effort || !effort) ? chosen.verdict : null;
  const { verdict, checking } = useVerdict(advanced || !known ? spec : null, machine, known);
  const blocked = verdictBlocks(verdict);
  const unchanged = sameSpec(spec, startSpec) && (!advanced || sameSpec(route, startSpec));
  const launcher = verdict?.launcher || (!advanced ? chosen?.launcher : undefined) || current.launcher;

  const pickModel = (m: string) => {
    setModel(m);
    const es = pickEfforts(picks, m);
    if (!es.includes(effort)) setEffort(es[0] ?? "");
  };
  const toggleAdvanced = () => {
    if (!advanced) setRoute(cleanSpec({ ...(chosen?.spec ?? startSpec), model, effort }));
    setAdvanced((a) => !a);
  };
  const run = async (pick: SpecPick) => {
    setBusy(true);
    try {
      if (await onApply(pick)) onClose();
    } finally {
      setBusy(false);
    }
  };
  const confirm = () => void run(pickOfSpec(verdict?.spec && advanced ? { ...verdict.spec, ...cleanSpec(route) } : spec, launcher, current.launcher));
  const replace = (alt: ExecutionSpec) => {
    setAdvanced(true);
    setRoute(cleanSpec(alt));
  };
  const restore = async () => {
    if (!kept) return;
    setBusy(true);
    try {
      const r = await restoreConversationLastGood(kept);
      if (!r) {
        setLastGood(null);
        onNotice(t("execution.restore_none"), "info");
        return;
      }
      if (r.model_pick) onRestored?.(r.model_pick);
      onNotice(t("execution.restored"), "ok");
      onClose();
    } catch (e) {
      onNotice(t("execution.restore_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };
  const lastGoodText = useMemo(() => (lastGood ? specText(catalog, { model: lastGood.spec.model, effort: lastGood.spec.effort }) : ""), [lastGood, catalog]);

  return (
    <Dialog
      open
      onClose={onClose}
      title={t("execution.panel_title")}
      width={460}
      footer={
        <>
          <Button tone="quiet" testId="model-sheet-more" onClick={onMore} className="mr-auto">{t("execution.more_models")}</Button>
          <Button tone="quiet" onClick={onClose}>{t("common.cancel")}</Button>
          <Button tone="primary" testId="model-sheet-apply" busy={busy} disabled={busy || blocked || checking || unchanged} onClick={confirm}>{t("execution.apply")}</Button>
        </>
      }
    >
      <div data-model-sheet="" className="flex flex-col gap-3">
        {header && <div className="-mx-1 overflow-hidden rounded-control border border-line">{header}</div>}
        {!advanced && (
          <div className="grid grid-cols-1 gap-3 xs:grid-cols-2">
            <label className="block min-w-0">
              <span className="mb-1 block text-label font-medium text-ink-2">{t("execution.model")}</span>
              <select data-testid="sheet-model" aria-label={t("execution.model")} className={routeInputClass} value={model} disabled={busy} onChange={(e) => pickModel(e.target.value)}>
                {models.map((m) => (
                  <option key={m} value={m}>{modelLabel(catalog, m)}</option>
                ))}
              </select>
            </label>
            <label className="block min-w-0">
              <span className="mb-1 block text-label font-medium text-ink-2">{t("execution.effort")}</span>
              <select data-testid="sheet-effort" aria-label={t("execution.effort")} className={routeInputClass} value={effort} disabled={busy || efforts.length === 0} onChange={(e) => setEffort(e.target.value)}>
                {efforts.length === 0 && <option value="">{t("execution.automatic")}</option>}
                {efforts.map((e) => (
                  <option key={e} value={e}>{e ? effortTitle(e) : t("execution.automatic")}</option>
                ))}
              </select>
            </label>
          </div>
        )}
        <button type="button" data-testid="sheet-advanced" aria-expanded={advanced} onClick={toggleAdvanced} className="touch-hit self-start text-label font-medium text-accent-ink hover:underline">
          {advanced ? t("execution.advanced_hide") : t("execution.advanced_show")}
        </button>
        {advanced && (
          <div className="flex flex-col gap-2">
            <p className="text-submeta text-ink-3">{t("execution.advanced_hint")}</p>
            <RouteFields catalog={catalog} machine={machine} spec={route} onChange={setRoute} disabled={busy} />
          </div>
        )}
        <VerdictNote verdict={verdict} catalog={catalog} onReplace={replace} busy={busy} />
        {kept && lastGood && (
          <div data-testid="sheet-last-good" className="flex flex-wrap items-center gap-2 rounded-control bg-inset px-3 py-2 text-label text-ink-2">
            <span className="min-w-0 flex-1 break-words">{t("execution.last_good", { setup: lastGoodText || t("execution.model_default") })}</span>
            <Button size="sm" testId="sheet-restore" disabled={busy} onClick={() => void restore()}>{t("execution.restore")}</Button>
          </div>
        )}
      </div>
    </Dialog>
  );
}
