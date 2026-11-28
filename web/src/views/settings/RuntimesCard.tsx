// Settings › Models › Engines (docs/web/web.md "Four dimensions in the Web"): the runtimes the hub declares (GET
// /execution/catalog) — what each speaks, its thinking efforts, what it switches without a restart — and which of the
// user's computers have it, online or not. Read only: a runtime comes with a computer's install, not from here.
import { t } from "../../i18n";
import { runtimeLabel, runtimeReadiness, type ExecutionCatalog } from "../../lib/execution";
import { effortTitle } from "../../lib/session-actions";
import { Loading, Pill } from "../../ui/atoms";
import { useExecutionCatalog } from "../execution/hooks";
import { Group, SettingsHint } from "./SettingsParts";

const PROTOCOL_NAMES: Record<string, string> = {
  anthropic_messages: "Anthropic Messages",
  openai_responses: "OpenAI Responses",
  openai_chat: "OpenAI Chat",
  gemini_native: "Gemini",
  acp_native: "ACP",
};

export const protocolLabel = (p: string) => PROTOCOL_NAMES[p] ?? p;

export function RuntimesCard({ catalog: controlled }: { catalog?: ExecutionCatalog | null }) {
  const { catalog, loading, error, missing } = useExecutionCatalog(undefined, true, controlled);
  // an older hub has no catalog: the card is not there
  if (missing) return null;
  if (!catalog && !loading && error) return <Group title={t("execution.runtimes_title")} anchor="runtimes"><SettingsHint alert>{error}</SettingsHint></Group>;
  const live = (dims: string[]) => dims.map((d) => (d === "model" ? t("execution.model") : d === "effort" ? t("execution.effort") : d)).join(", ");
  return (
    <Group title={t("execution.runtimes_title")} footer={t("execution.runtimes_footer")} anchor="runtimes">
      {!catalog && <Loading />}
      {catalog && catalog.runtimes.length === 0 && <SettingsHint>{t("execution.runtimes_empty")}</SettingsHint>}
      {catalog?.runtimes.map((r) => {
        const ready = runtimeReadiness(catalog, r.runtime);
        const total = ready.online.length + ready.offline.length;
        return (
          <div key={r.runtime} data-runtime={r.runtime} className="flex flex-col gap-1.5 px-4 py-3">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-meta font-medium text-ink">{runtimeLabel(r.runtime)}</span>
              <span className="font-mono text-submeta text-ink-3">{r.runtime}</span>
              <span className="flex-1" />
              <Pill tone={ready.online.length ? "green" : total ? "orange" : undefined} size="sm">
                {total ? t("execution.runtime_ready", { online: ready.online.length, total }) : t("execution.runtime_nowhere")}
              </Pill>
            </div>
            {(r.protocols?.length ?? 0) > 0 && (
              <div className="text-submeta text-ink-2">{t("execution.runtime_protocols", { protocols: (r.protocols ?? []).map(protocolLabel).join(", ") })}</div>
            )}
            {(r.efforts?.length ?? 0) > 0 && (
              <div className="text-submeta text-ink-3">{t("execution.runtime_efforts", { efforts: (r.efforts ?? []).map(effortTitle).join(", ") })}</div>
            )}
            {(r.live_switch?.length ?? 0) > 0 && (
              <div className="text-submeta text-ink-3">{t("execution.runtime_live_switch", { dimensions: live(r.live_switch ?? []) })}</div>
            )}
            {total > 0 && (
              <div className="flex flex-wrap gap-1">
                {[...ready.online, ...ready.offline].map((m) => (
                  <Pill key={m.machine} tone={m.online ? undefined : "orange"} size="sm">
                    {m.online ? (m.name || m.machine) : t("execution.machine_offline", { machine: m.name || m.machine })}
                  </Pill>
                ))}
              </div>
            )}
          </div>
        );
      })}
    </Group>
  );
}
