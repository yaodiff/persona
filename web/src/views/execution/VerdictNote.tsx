// What the hub says of one combination (POST /execution/validate, docs/assistant/execution-spec.md): nothing when it
// is allowed; a quiet line per reason when it runs with a notice, an experimental bridge marked as such; the first
// reason in red when it is blocked, with each alternative the hub offers as a one-tap replacement. The words are the
// hub's (`reasons[].text`, in the request's language); the code only picks the icon.
import { t } from "../../i18n";
import { bridgedExperimental, channelLabel, modelLabel, runtimeLabel, type CombinationVerdict, type ExecutionCatalog, type ExecutionSpec } from "../../lib/execution";
import { effortTitle } from "../../lib/session-actions";
import { Button, Pill } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";

/** An alternative in words: model · effort · engine · channel (the dimensions it names). */
export function specText(catalog: ExecutionCatalog | null | undefined, spec: ExecutionSpec): string {
  return [
    spec.model ? modelLabel(catalog, spec.model) : "",
    spec.effort ? effortTitle(spec.effort) : "",
    runtimeLabel(spec.runtime),
    channelLabel(catalog, spec.channel),
  ].filter(Boolean).join(" · ");
}

const ICON: Record<string, IconName> = {
  bridged_experimental: "sparkles",
  bridge_warning: "warning",
  effort_clamped: "sliders",
  effort_ignored: "sliders",
  machine_offline: "machine",
  readiness_unknown: "info",
  model_unlisted: "info",
};

export function VerdictNote({ verdict, catalog, onReplace, busy }: {
  verdict: CombinationVerdict | null | undefined;
  catalog?: ExecutionCatalog | null;
  /** One alternative was tapped: the form takes it. Absent: the alternatives are only named. */
  onReplace?: (spec: ExecutionSpec) => void;
  busy?: boolean;
}) {
  if (!verdict || verdict.level === "allowed") return null;
  const reasons = verdict.reasons ?? [];
  if (verdict.level === "blocked") {
    const first = reasons[0];
    const alternatives = verdict.alternatives ?? [];
    return (
      <div role="alert" data-verdict="blocked" className="flex flex-col gap-2 rounded-control bg-inset px-3 py-2 text-label text-red">
        <div className="flex items-start gap-2">
          <Icon name="alertCircle" size={13} stroke={2} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">{first?.text || t("execution.blocked_generic")}</span>
        </div>
        {alternatives.length > 0 && (
          <div className="flex flex-wrap items-center gap-1.5 text-ink-2">
            <span>{t("execution.alternatives")}</span>
            {alternatives.map((alt, i) => (
              <Button key={i} size="sm" tone="secondary" disabled={busy || !onReplace} testId={`verdict-alternative-${i}`} onClick={() => onReplace?.(alt)}>
                {specText(catalog, alt) || t("execution.model_default")}
              </Button>
            ))}
          </div>
        )}
      </div>
    );
  }
  const bridged = bridgedExperimental(verdict);
  const texts = [...new Map(reasons.map((r) => [r.text, r])).values()];
  return (
    <div role="status" data-verdict="notice" className="flex flex-col gap-1 rounded-control bg-inset px-3 py-2 text-label text-ink-2">
      {bridged && (
        <span data-bridged-experimental="" className="self-start">
          <Pill tone="orange" size="sm">{t("execution.bridged_experimental")}</Pill>
        </span>
      )}
      {texts.map((r) => (
        <div key={r.text} className="flex items-start gap-2">
          <Icon name={ICON[r.code] ?? "info"} size={12} stroke={2} className="mt-0.5 shrink-0 text-ink-3" />
          <span className="min-w-0 flex-1 break-words">{r.text}</span>
        </div>
      ))}
    </div>
  );
}
