// The "Advanced / route" fields (docs/assistant/execution-spec.md, the stage 3 contract): engine, channel, model and
// effort, each "Automatic" (inherited) by default. The choices narrow one another: the engines are the catalog's, the
// channels are the computer's that run the engine, the models are the channel's, the efforts the model's (else the
// engine's). A value no longer offered stays shown, so nothing changes under the user's hand.
import { useId } from "react";
import { t } from "../../i18n";
import {
  channelLabel, channelOptions, effortOptions, modelLabel, modelOptions, narrowSpec, runtimeLabel, runtimeOptions,
  type ExecutionCatalog, type ExecutionSpec,
} from "../../lib/execution";
import { effortTitle } from "../../lib/session-actions";

/** The same field as Settings' model pickers (views/settings/ModelBindingPickers.tsx pickerInputClass). */
export const routeInputClass =
  "h-9 w-full rounded-control bg-field px-3 text-meta text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";

export type RouteDimension = "runtime" | "channel" | "model" | "effort";

function Field({ label, value, options, onChange, disabled, testId }: {
  label: string;
  value: string;
  options: { id: string; label: string }[];
  onChange: (value: string) => void;
  disabled?: boolean;
  testId: string;
}) {
  const id = useId();
  const shown = value && !options.some((o) => o.id === value) ? [{ id: value, label: value }, ...options] : options;
  return (
    <label htmlFor={id} className="block min-w-0">
      <span className="mb-1 block text-label font-medium text-ink-2">{label}</span>
      <select id={id} data-testid={testId} aria-label={label} className={routeInputClass} value={value} disabled={disabled} onChange={(e) => onChange(e.target.value)}>
        <option value="">{t("execution.automatic")}</option>
        {shown.map((o) => (
          <option key={o.id} value={o.id}>{o.label}</option>
        ))}
      </select>
    </label>
  );
}

export function RouteFields({ catalog, machine, spec, onChange, dimensions = ["runtime", "channel", "model", "effort"], disabled }: {
  catalog: ExecutionCatalog | null;
  machine?: string;
  spec: ExecutionSpec;
  onChange: (spec: ExecutionSpec) => void;
  /** The fields shown, in this order (a form whose own pickers choose the model and effort shows only the route). */
  dimensions?: RouteDimension[];
  disabled?: boolean;
}) {
  const set = (patch: ExecutionSpec) => onChange(narrowSpec(catalog, machine, spec, patch));
  const runtimes = runtimeOptions(catalog).map((r) => ({ id: r, label: runtimeLabel(r) }));
  const channels = channelOptions(catalog, machine, spec.runtime).map((c) => ({ id: c.name, label: channelLabel(catalog, c.name) }));
  const models = modelOptions(catalog, spec.channel).map((m) => ({ id: m.canonical, label: modelLabel(catalog, m.canonical) }));
  const efforts = effortOptions(catalog, spec.channel, spec.model, spec.runtime).map((e) => ({ id: e, label: effortTitle(e) }));
  return (
    <div data-route-fields="" className="grid grid-cols-1 gap-3 sm:grid-cols-2">
      {dimensions.map((d) =>
        d === "runtime" ? (
          <Field key={d} testId="route-runtime" label={t("execution.runtime")} value={spec.runtime ?? ""} options={runtimes} disabled={disabled} onChange={(runtime) => set({ runtime })} />
        ) : d === "channel" ? (
          <Field key={d} testId="route-channel" label={t("execution.channel")} value={spec.channel ?? ""} options={channels} disabled={disabled} onChange={(channel) => set({ channel })} />
        ) : d === "model" ? (
          <Field key={d} testId="route-model" label={t("execution.model")} value={spec.model ?? ""} options={models} disabled={disabled} onChange={(model) => set({ model })} />
        ) : (
          <Field key={d} testId="route-effort" label={t("execution.effort")} value={spec.effort ?? ""} options={efforts} disabled={disabled} onChange={(effort) => set({ effort })} />
        ),
      )}
    </div>
  );
}
