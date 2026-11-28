// "Engine" on a machine's page (docs/integrations/engines.md, iOS EngineSelectionView): which terminal engine this computer's sessions
// come from. The engines its node found ("Available" with the version / "Not detected", and why one cannot be used), or every known
// engine while it is offline; a choice stays here until "Save" (PUT …/engine), which the hub takes offline too. Pushes
// of the machine refresh the choice until the user starts changing it; a failed save keeps it for another try.
import { useLayoutEffect, useRef, useState } from "react";
import { api, enc, errorText } from "../../lib/api";
import {
  canSaveEngine,
  canSelectEngine,
  currentEngine,
  detectionText,
  engineDetail,
  engineFooter,
  engineName,
  engineOptions,
  engineSaveError,
  supportsHostSets,
} from "../../lib/engines";
import { applyMachine } from "../../lib/store";
import type { Machine } from "../../lib/types";
import { Button, cx, EASE, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { EngineHostsSection } from "./EngineHostsSection";
import { OrcaChatStarts } from "./OrcaChatStarts";
import { ENGINE_SECTION_ID } from "./engine-anchor";
import { beginEngineSave, finishEngineSave, isLatestEngineSave } from "./engine-save";
import { t } from "../../i18n";

export function EngineSection({ machine, heading }: { machine: Machine; heading: React.ReactNode }) {
  // A hub with sets (#2576): several engines checked side by side plus a default; an older hub, one engine.
  if (supportsHostSets(machine)) return <EngineHostsSection key={machine.id} machine={machine} heading={heading} />;
  return <SingleEngineSection key={machine.id} machine={machine} heading={heading} />;
}

function SingleEngineSection({ machine, heading }: { machine: Machine; heading: React.ReactNode }) {
  const current = currentEngine(machine);
  const [prevMachineId, setPrevMachineId] = useState(machine.id);
  const [prevCurrent, setPrevCurrent] = useState(current);
  const [selected, setSelected] = useState(current);
  const [edited, setEdited] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  if (prevMachineId !== machine.id) {
    setPrevMachineId(machine.id);
    setPrevCurrent(current);
    setSelected(current);
    setEdited(false);
    setSaving(false);
    setError("");
  } else if (!saving && prevCurrent !== current) {
    setPrevCurrent(current);
    if (!edited) {
      setSelected(current);
    } else if (selected === current) {
      // Remote state converged to the unsaved draft: clear the latch so future pushes follow the hub.
      setEdited(false);
      setError("");
    }
  }
  const machineIdRef = useRef(machine.id);
  const saveSeqRef = useRef(0);
  const savingRef = useRef(false);
  const mountedRef = useRef(true);

  useLayoutEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      savingRef.current = false;
    };
  }, []);

  useLayoutEffect(() => {
    if (machineIdRef.current !== machine.id) {
      machineIdRef.current = machine.id;
      saveSeqRef.current += 1;
      savingRef.current = false;
    }
  }, [machine.id]);

  const choose = (id: string) => {
    if (savingRef.current) return;
    setSelected(id);
    setEdited(id !== current);
    setError("");
  };

  const save = async () => {
    if (savingRef.current) return;
    const targetId = machine.id;
    const seq = ++saveSeqRef.current;
    const saveId = beginEngineSave(targetId);
    savingRef.current = true;
    setSaving(true);
    setError("");
    const isCurrent = () => mountedRef.current && machineIdRef.current === targetId && saveSeqRef.current === seq;
    try {
      const saved = await api<Machine>(`/machines/${enc(targetId)}/engine`, { method: "PUT", json: { engine: selected } });
      if (isLatestEngineSave(targetId, saveId)) applyMachine(saved);
      if (!isCurrent()) return;
      setSelected(currentEngine(saved));
      setEdited(false);
      toast(t("machines-enginesection.saved", { engine: engineName(currentEngine(saved), saved.engines?.length ? saved : machine) }), "ok");
    } catch (e) {
      if (!isCurrent()) return;
      setError(engineSaveError(errorText(e)));
    } finally {
      finishEngineSave(targetId, saveId);
      if (isCurrent()) {
        savingRef.current = false;
        setSaving(false);
      }
    }
  };

  const options = engineOptions(machine);
  const dirty = canSaveEngine(selected, machine);
  return (
    <section id={ENGINE_SECTION_ID} tabIndex={-1} data-testid="engine-section" className="scroll-mt-4 outline-none">
      {heading}
      <div className="overflow-hidden rounded-card bg-surface shadow-card" style={{ animation: `fade-up 420ms ${EASE} both` }}>
        <div role="radiogroup" aria-label={t("machines-enginesection.label")} className="divide-y divide-line">
          {options.map((o) => {
            const on = selected === o.id;
            const selectable = canSelectEngine(o.id, machine);
            const detail = engineDetail(o.id);
            const found = machine.online && machine.engines?.find((e) => e.id === o.id);
            return (
              <button
                key={o.id}
                type="button"
                role="radio"
                aria-checked={on}
                data-engine={o.id}
                disabled={saving || !selectable}
                onClick={() => choose(o.id)}
                className={cx("flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors enabled:hover:bg-hover disabled:cursor-default", !selectable && "opacity-60")}
              >
                <span className="min-w-0 flex-1">
                  <span className="flex min-w-0 items-baseline gap-2">
                    <span className="truncate text-[13px] font-medium text-ink">{o.name}</span>
                    {detail && <span className="hidden min-w-0 truncate text-[11.5px] text-ink-3 sm:inline">{detail}</span>}
                    {o.id === current && <span className="inline-flex h-[18px] shrink-0 items-center whitespace-nowrap rounded-full bg-inset px-1.5 text-[10.5px] font-medium text-ink-2 shadow-hairline">{t("machines-enginesection.current")}</span>}
                  </span>
                  <span className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[11.5px] text-ink-3">
                    <span className={cx("size-1.5 shrink-0 rounded-full", found && found.available ? "bg-green" : "bg-line-strong")} />
                    <span className="truncate">{detectionText(o.id, machine)}</span>
                  </span>
                  {o.note && <span className="mt-0.5 block truncate text-[11.5px] text-orange" title={o.note}>{o.note}</span>}
                </span>
                <span
                  aria-hidden
                  className={cx(
                    "flex size-[18px] shrink-0 items-center justify-center rounded-full transition-[background-color,box-shadow] duration-150",
                    on ? "bg-accent text-white" : "shadow-[inset_0_0_0_1.5px_var(--line-strong)]",
                  )}
                >
                  {on && <Icon name="check" size={11} stroke={3} />}
                </span>
              </button>
            );
          })}
        </div>
        {(dirty || error) && (
          <div className="flex flex-wrap items-center gap-2 border-t border-line px-4 py-2.5">
            <p className={cx("min-w-0 flex-1 text-[12px]", error ? "text-red" : "text-ink-2")} role={error ? "alert" : undefined}>
              {error || t("machines-enginesection.change_to", { engine: engineName(selected, machine) })}
            </p>
            <Button tone="quiet" disabled={saving} onClick={() => choose(current)}>
              {t("machines-enginesection.revert")}
            </Button>
            <Button tone="primary" busy={saving} disabled={!canSelectEngine(selected, machine)} onClick={() => void save()}>
              {t("common.save")}
            </Button>
          </div>
        )}
      </div>
      <p className="mt-2 px-1 text-[11.5px] leading-relaxed text-ink-3">{engineFooter(selected, machine.online)}</p>
      <OrcaChatStarts machine={machine} />
    </section>
  );
}
