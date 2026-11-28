// Read-only catalog and detail for the lazy Settings Integrations view.
import { enc } from "./api";
import {
  INTEGRATION_CONFIGURATION_GROUPS, INTEGRATION_FILTER_CAPABILITIES, INTEGRATION_KINDS, INTEGRATION_ORIGINS,
  INTEGRATION_SECTION_EDITS, INTEGRATION_SECTION_KINDS, INTEGRATION_SOURCES,
  type IntegrationCatalog, type IntegrationCatalogItem, type IntegrationDetail, type IntegrationKind, type IntegrationRef,
  type IntegrationSourceAvailability,
} from "./integrations-projection-wire";
import type { ManagerList } from "./managers";
import { AssistantScopeUnverified, type ScopedAssistantClient } from "./scoped-assistant-api";

const path = "/assistant/integrations";

export function supportsIntegrationsProjection(roster: ManagerList | null, manager: string): boolean {
  return roster?.managers.find((entry) => entry.id === manager)?.integrations_projection_v1 === true;
}

function assertOwner(ref: IntegrationRef, manager: string) {
  if (ref?.manager !== manager || typeof ref.id !== "string" || !ref.id) throw new AssistantScopeUnverified();
}

function decodeItem(item: IntegrationCatalogItem, manager: string): IntegrationCatalogItem | null {
  assertOwner(item?.ref, manager);
  if (!INTEGRATION_KINDS.includes(item.ref.kind) || !INTEGRATION_ORIGINS.includes(item.origin)) return null;
  if (typeof item.name !== "string" || !item.availability?.state || !Array.isArray(item.capabilities)) throw new AssistantScopeUnverified();
  return {
    ...item,
    capabilities: item.capabilities.filter((cap) => INTEGRATION_FILTER_CAPABILITIES.includes(cap)),
    capability_availability: item.capability_availability?.filter(({ capability }) => INTEGRATION_FILTER_CAPABILITIES.includes(capability)),
  };
}

function decodeAvailability(sources: IntegrationSourceAvailability[], manager: string): IntegrationSourceAvailability[] {
  if (!Array.isArray(sources)) throw new AssistantScopeUnverified();
  return sources.filter(({ ref, source }) => {
    if (ref) assertOwner(ref, manager);
    return INTEGRATION_SOURCES.includes(source) && (!ref || INTEGRATION_KINDS.includes(ref.kind));
  });
}

export async function readIntegrationsCatalog(client: ScopedAssistantClient): Promise<IntegrationCatalog> {
  const result = await client.request<IntegrationCatalog>(path);
  if (!Array.isArray(result?.items) || !result.counts) throw new AssistantScopeUnverified();
  const items = result.items.flatMap((item) => decodeItem(item, client.managerId) ?? []);
  return { items, counts: result.counts, availability: decodeAvailability(result.availability, client.managerId) };
}

export async function readIntegrationDetail(client: ScopedAssistantClient, kind: IntegrationKind, id: string): Promise<IntegrationDetail> {
  const result = await client.request<IntegrationDetail>(`${path}/${enc(kind)}/${enc(id)}`);
  if (!Array.isArray(result?.sections)) throw new AssistantScopeUnverified();
  const item = decodeItem(result.item, client.managerId);
  const sections = result.sections.flatMap((section) => {
    assertOwner(section.ref, client.managerId);
    if (!INTEGRATION_SECTION_KINDS.includes(section.kind) || !INTEGRATION_KINDS.includes(section.ref.kind) ||
      !INTEGRATION_SECTION_EDITS.includes(section.edit)) return [];
    return [{ ...section, configuration_groups: section.configuration_groups?.filter((group) => INTEGRATION_CONFIGURATION_GROUPS.includes(group)) }];
  });
  // The assistant may resolve a legacy channel, connector, or subscription link to its owning integration.
  const requested = (ref: IntegrationRef) => ref.kind === kind && ref.id === id;
  if (!item || (!requested(item.ref) && !sections.some(({ ref }) => requested(ref)))) throw new AssistantScopeUnverified();
  return { item, sections, availability: decodeAvailability(result.availability, client.managerId) };
}
