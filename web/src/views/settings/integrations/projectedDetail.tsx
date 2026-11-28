import { t } from "../../../i18n";
import {
  INTEGRATION_STATES, type IntegrationDetail, type IntegrationKind, type IntegrationSection, type IntegrationSectionKind,
} from "../../../lib/integrations-projection-wire";
import { Button } from "../../../ui/atoms";
import { Group, SettingsHint } from "../SettingsParts";
import type { IntegrationTarget } from "./model";
import { capabilityLabel, projectionSourceErrors } from "./projectedCatalog";

export interface ProjectedDetail {
  detail: IntegrationDetail;
  refresh: () => void;
  loading: boolean;
}

function readable(section: IntegrationSection) {
  return INTEGRATION_STATES.includes(section.availability.state) && section.availability.state !== "module_off" &&
    section.availability.reason !== "read_failed";
}

export function editableSection(section: IntegrationSection): boolean {
  return readable(section) && section.edit === "settings" && section.availability.state !== "read_only";
}

export function projectedSections(projection: ProjectedDetail | undefined, kind: IntegrationSectionKind, resource?: IntegrationKind) {
  return projection?.detail.sections.filter((section) => section.kind === kind &&
    (!resource || section.ref.kind === resource) && readable(section)) ?? [];
}

export function projectedIntegrationTarget(projection: ProjectedDetail): IntegrationTarget | null {
  const { ref, name } = projection.detail.item;
  const target = { id: ref.id, manager: ref.manager, name };
  if (ref.kind === "extension") return { kind: "extension", ...target };
  if (ref.kind === "push_source" || ref.kind === "subscription") return null;
  return {
    kind: "builtin",
    ...target,
    channelId: projectedSections(projection, "conversation_and_push", "channel")[0]?.ref.id,
    connectorId: projectedSections(projection, "connection", "connector")[0]?.ref.id ?? null,
  };
}

const sectionLabels: Record<IntegrationSectionKind, string> = {
  connection: "settings-integrations.section_connection",
  conversation_and_push: "settings-integrations.section_channel",
  inbox_and_sync: "settings-integrations.section_inbox",
  external_sources: "settings-integrations.filter_data",
  extension_configuration: "settings-integrations.section_extension_settings",
  information_access: "settings-extensioninfoaccess.title",
  lifecycle: "settings-integrations.section_management",
};

export function IntegrationReadProblem({ text, retry, title, busy }: { text: string; retry: () => void; title?: string; busy?: boolean }) {
  return (
    <Group title={title}>
      <SettingsHint alert>{text}</SettingsHint>
      <div className="px-4 py-2.5">
        <Button className="min-h-11" onClick={retry} busy={busy}>{t("sidebar.retry")}</Button>
      </div>
    </Group>
  );
}

export function ProjectionDetailIssues({ projection }: { projection?: ProjectedDetail }) {
  if (!projection) return null;
  const { detail, refresh, loading } = projection;
  const sections = detail.sections.filter(({ availability }) => availability.reason === "read_failed");
  const capabilities = detail.item.capability_availability?.filter(({ availability }) => availability.reason === "read_failed") ?? [];
  const labels = new Set([
    ...sections.map(({ kind }) => t(sectionLabels[kind])),
    ...capabilities.map(({ capability }) => t(capabilityLabel[capability])),
    ...projectionSourceErrors(detail.availability),
  ]);
  if (!labels.size && detail.item.availability.reason !== "read_failed") return null;

  return (
    <IntegrationReadProblem title={labels.size > 0 ? [...labels].join(" · ") : undefined}
      text={t("settings-integrations.projection_section_error")} retry={refresh} busy={loading} />
  );
}
