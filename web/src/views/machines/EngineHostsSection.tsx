// "Engine" on a machine's page with a hub that takes sets (#2576, docs/integrations/engines.md "Host sets"): every engine is a checkbox
// (several may be on at once, or none), and one of the checked ones is the default, where new sessions open. Same row
// shape as the single-engine picker (EngineSection); a choice stays here until "Save" (PUT …/engine {hosts, default}).
import { useLayoutEffect, useRef, useState } from "react";
import { api, enc, errorText } from "../../lib/api";
import {
  currentHosts,
  detectionText,
  engineDetail,
  engineSaveError,
  hostOptions,
  hostsName,
  hostsFooter,
  sameHosts,
  toggleHost,
  type HostChoice,
} from "../../lib/engines";
import { applyMachine } from "../../lib/store";
import type { Machine } from "../../lib/types";
import { Button, cx, EASE, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { ENGINE_SECTION_ID } from "./engine-anchor";
import { beginEngineSave, finishEngineSave, isLatestEngineSave } from "./engine-save";
import { t } from "../../i18n";
import { OrcaChatStarts } from "./OrcaChatStarts";

export function EngineHostsSection({ machine, heading }: { machine: Machine; heading: React.ReactNode }) {
  const current = currentHosts(machine);
  const currentKey = `${current.hosts.join(",")}|${current.default}`;
  const [prevMachineId, setPrevMachineId] = useState(machine.id);
  const [prevCurrentKey, setPrevCurrentKey] = useState(currentKey);
  const [choice, setChoice] = useState<HostChoice>(current);
  const [edited, setEdited] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  if (prevMachineId !== machine.id) {
    setPrevMachineId(machine.id);
    setPrevCurrentKey(currentKey);
    setChoice(current);
    setEdited(false);
    setSaving(false);
    setError("");
  } else if (!saving && prevCurrentKey !== currentKey) {
    setPrevCurrentKey(currentKey);
    if (!edited) {
      if (!sameHosts(choice, current)) setChoice(current);
    } else if (sameHosts(choice, current)) {
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

  const change = (next: HostChoice) => {
    if (savingRef.current) return;
    setChoice(next);
    setEdited(!sameHosts(next, current));
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
      const saved = await api<Machine>(`/machines/${enc(targetId)}/engine`, {
        method: "PUT",
        json: { hosts: choice.hosts, default: choice.default },
      });
      if (isLatestEngineSave(targetId, saveId)) applyMachine(saved);
      if (!isCurrent()) return;
      setChoice(currentHosts(saved));
      setEdited(false);
      toast(t("machines-enginesection.saved", { engine: hostsName(currentHosts(saved), saved.engines?.length ? saved : machine) }), "ok");
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

  const dirty = !sameHosts(choice, current);
  return (
    <section id={ENGINE_SECTION_ID} tabIndex={-1} data-testid="engine-section" data-engine-sets className="scroll-mt-4 outline-none">
      {heading}
      <div className="overflow-hidden rounded-card bg-surface shadow-card" style={{ animation: `fade-up 420ms ${EASE} both` }}>
        <div role="group" aria-label={t("machines-enginesection.label")} className="divide-y divide-line">
          {hostOptions(machine).map((o) => {
            const on = choice.hosts.includes(o.id);
            const isDefault = on && choice.default === o.id;
            const detail = engineDetail(o.id);
            const found = machine.online && machine.engines?.find((e) => e.id === o.id);
            return (
              <div key={o.id} data-engine={o.id} className="flex w-full items-center gap-3 px-4 py-2.5">
                <button
                  type="button"
                  role="checkbox"
                  aria-checked={on}
                  disabled={saving}
                  onClick={() => change(toggleHost(choice, o.id))}
                  className="flex min-w-0 flex-1 items-center gap-3 text-left disabled:cursor-default"
                >
                  <span
                    aria-hidden
                    className={cx(
                      "flex size-[18px] shrink-0 items-center justify-center rounded-chip transition-[background-color,box-shadow] duration-150",
                      on ? "bg-accent text-white" : "shadow-[inset_0_0_0_1.5px_var(--line-strong)]",
                    )}
                  >
                    {on && <Icon name="check" size={11} stroke={3} />}
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="flex min-w-0 items-baseline gap-2">
                      <span className="truncate text-[13px] font-medium text-ink">{o.name}</span>
                      {detail && <span className="hidden min-w-0 truncate text-[11.5px] text-ink-3 sm:inline">{detail}</span>}
                    </span>
                    <span className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[11.5px] text-ink-3">
                      <span className={cx("size-1.5 shrink-0 rounded-full", found && found.available ? "bg-green" : "bg-line-strong")} />
                      <span className="truncate">{detectionText(o.id, machine)}</span>
                    </span>
                    {o.note && <span className="mt-0.5 block truncate text-[11.5px] text-orange" title={o.note}>{o.note}</span>}
                  </span>
                </button>
                {isDefault && (
                  <span className="inline-flex h-[18px] shrink-0 items-center whitespace-nowrap rounded-full bg-inset px-1.5 text-[10.5px] font-medium text-ink-2 shadow-hairline">
                    {t("lib-engines.default")}
                  </span>
                )}
                {on && !isDefault && (
                  <Button tone="quiet" disabled={saving} onClick={() => change({ ...choice, default: o.id })}>
                    <span className="whitespace-nowrap">{t("lib-engines.make_default")}</span>
                  </Button>
                )}
              </div>
            );
          })}
        </div>
        {(dirty || error) && (
          <div className="flex flex-wrap items-center gap-2 border-t border-line px-4 py-2.5">
            <p className={cx("min-w-0 flex-1 text-[12px]", error ? "text-red" : "text-ink-2")} role={error ? "alert" : undefined}>
              {error || t("machines-enginesection.change_to", { engine: hostsName(choice, machine) })}
            </p>
            <Button tone="quiet" disabled={saving} onClick={() => change(current)}>
              {t("machines-enginesection.revert")}
            </Button>
            <Button tone="primary" busy={saving} onClick={() => void save()}>
              {t("common.save")}
            </Button>
          </div>
        )}
      </div>
      {machine.engine_sets_unsupported && (
        <p className="mt-2 px-1 text-[11.5px] leading-relaxed text-orange" role="status">
          {t("lib-engines.sets_unsupported")}
        </p>
      )}
      <p className="mt-2 px-1 text-[11.5px] leading-relaxed text-ink-3">{hostsFooter(choice, machine.online)}</p>
      <OrcaChatStarts machine={machine} />
    </section>
  );
}
