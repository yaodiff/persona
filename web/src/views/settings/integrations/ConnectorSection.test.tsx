// @vitest-environment jsdom
// Unit & component tests for ConnectorSection's explicit host-auth contract (#17803).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../../i18n";
import type { HubConnector } from "../../../lib/connectors";
import { ConnectorConnectionBlock, showHubSideNote } from "./ConnectorSection";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const HUB_CONFIGURED_CONNECTOR: HubConnector = {
  id: "storefront",
  name: "Storefront",
  status: "not_signed_in",
  enabled: true,
  capabilities: ["read"],
};

describe("showHubSideNote", () => {
  it("shows hub_side_note for standalone hub-configured connectors without login capability", () => {
    expect(showHubSideNote(HUB_CONFIGURED_CONNECTOR)).toBe(true);
    expect(showHubSideNote(HUB_CONFIGURED_CONNECTOR, false)).toBe(true);
  });

  it("hides hub_side_note when the host integration already manages interactive auth", () => {
    expect(showHubSideNote(HUB_CONFIGURED_CONNECTOR, true)).toBe(false);
  });

  it("hides hub_side_note for iMessage, login-capable connectors, and undescribed connectors", () => {
    expect(showHubSideNote({ ...HUB_CONFIGURED_CONNECTOR, id: "imessage" }, false)).toBe(false);
    expect(showHubSideNote({ ...HUB_CONFIGURED_CONNECTOR, capabilities: ["login", "read"] }, false)).toBe(false);
    expect(showHubSideNote({ ...HUB_CONFIGURED_CONNECTOR, capabilities: [] }, false)).toBe(false);
  });
});

describe("ConnectorConnectionBlock", () => {
  let host: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
  });

  afterEach(() => {
    act(() => root.unmount());
    host.remove();
  });

  it("renders hub_side_note by default when auth is not managed by the host", () => {
    act(() => {
      root.render(
        <ConnectorConnectionBlock
          connector={HUB_CONFIGURED_CONNECTOR}
          reload={vi.fn(async () => {})}
        />,
      );
    });

    expect(host.textContent).toContain(t("settings-integrations.connector_status_title"));
    expect(host.textContent).toContain(t("lib-connectors.state_not_signed_in"));
    expect(host.textContent).toContain(t("settings-hubconnector.hub_side_note"));
  });

  it("suppresses hub_side_note when authManagedByHost is true while keeping the status card", () => {
    act(() => {
      root.render(
        <ConnectorConnectionBlock
          connector={HUB_CONFIGURED_CONNECTOR}
          reload={vi.fn(async () => {})}
          authManagedByHost
        />,
      );
    });

    expect(host.textContent).toContain(t("settings-integrations.connector_status_title"));
    expect(host.textContent).toContain(t("lib-connectors.state_not_signed_in"));
    expect(host.textContent).not.toContain(t("settings-hubconnector.hub_side_note"));
  });
});
