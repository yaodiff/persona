import { afterEach, expect, it, vi } from "vitest";
import { api, ApiError } from "./api";
import { MANAGER_HEADER } from "./manager-choice";
import { parseDocument } from "./configuration/contract";
import { integrationsV2LabsSource } from "./labs-integrations-v2";
import { t } from "../i18n";

vi.mock("./api", async (original) => ({ ...await original<typeof import("./api")>(), api: vi.fn() }));
afterEach(() => vi.resetAllMocks());

it("binds the Labs read, save and reset to the manager whose form was opened", async () => {
  const manager = "example";
  const source = integrationsV2LabsSource(manager);
  expect(source.key).toBe(`labs:assistant-integrations-v2:${manager}`);
  const request = vi.mocked(api);
  request.mockResolvedValueOnce({ manager, integrations_v2: true });
  const { doc } = parseDocument(await source.read());
  expect(doc.id).toBe("assistant-integrations-v2");
  expect(doc.values.integrations_v2).toBe(true);
  expect(doc.groups[0].fields[0]).toMatchObject({ default: true });
  expect(doc.groups[0].fields[0].details?.map((d) => d.title)).toEqual([
    t("settings-labs.scope"),
    t("settings-labs.activation"),
    t("settings-labs.rollback"),
  ]);
  expect(request).toHaveBeenCalledWith("/assistant", expect.objectContaining({ headers: { [MANAGER_HEADER]: manager } }));
  request.mockResolvedValueOnce({ manager, integrations_v2: false });
  const { doc: saved } = parseDocument(await source.update({ ...doc, changes: { integrations_v2: false } }));
  expect(saved.values.integrations_v2).toBe(false);
  expect(saved.notices?.integrations_v2?.kind).toBe("info");
  expect(request).toHaveBeenLastCalledWith("/assistant", expect.objectContaining({
    headers: { [MANAGER_HEADER]: manager }, json: { integrations_v2: false, expected_integrations_v2: true },
  }));
  request.mockResolvedValueOnce({ manager, integrations_v2: true });
  expect(parseDocument(await source.update({ ...saved, reset: ["integrations_v2"] })).doc.values.integrations_v2).toBe(true);
  expect(request).toHaveBeenLastCalledWith("/assistant", expect.objectContaining({
    json: { integrations_v2: true, expected_integrations_v2: false },
  }));
});

it("leaves older managers read-only, reports unconfirmed saves, and surfaces 409 conflicts", async () => {
  const source = integrationsV2LabsSource("example");
  vi.mocked(api).mockResolvedValueOnce({ manager: "example" });
  const { doc } = parseDocument(await source.read());
  expect(doc.groups[0].fields[0].read_only).toBe(true);
  expect(doc.values.integrations_v2).toBe(false);
  expect(doc.groups[0].fields[0].disabled_reason).toBe(t("settings-labs.integrations_v2_unsupported"));

  vi.mocked(api).mockResolvedValueOnce({ manager: "other", integrations_v2: true });
  await expect(source.update({ schema_version: 1, id: doc.id, revision: "false", changes: { integrations_v2: true } }))
    .rejects.toMatchObject({ code: "configuration_unavailable" });

  vi.mocked(api).mockRejectedValueOnce(new ApiError(409, "integrations_v2 setting changed", { code: "configuration_conflict", error: "integrations_v2 setting changed" }));
  await expect(source.update({ schema_version: 1, id: doc.id, revision: "false", changes: { integrations_v2: true } }))
    .rejects.toMatchObject({ code: "configuration_conflict" });
});
