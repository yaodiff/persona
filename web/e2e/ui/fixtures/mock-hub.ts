import { test as base, expect, type Page } from "@playwright/test";

export const AT = "2026-11-28T08:00:00Z";
export const MACHINE = { id: "demo", name: "Demo Mac", online: true, os: "darwin", host: "herdr" } as const;
export const MAIN_ID = "main-chat";
export const AGENT_ID = "agent-side";

export const MAIN_SESSION = {
  machine_id: MACHINE.id,
  session_id: MAIN_ID,
  title: "Persona",
  state: "idle",
  status: "idle",
  cwd: "/demo",
  updated_at: AT,
  started_by_persona: true,
  assistant: true,
  capabilities: { send: true, interrupt: true, approve: true },
};

export const AGENT_SESSION = {
  machine_id: MACHINE.id,
  session_id: AGENT_ID,
  title: "Inspect split panel",
  name: "Inspect split panel",
  state: "working",
  status: "working",
  agent: "claude",
  online: true,
  cwd: "/demo/project",
  updated_at: AT,
  state_since: AT,
  pending_interactions: 0,
  capabilities: { send: true, interrupt: true, approve: true },
};

export const PNG_1X1 = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=",
  "base64",
);

export interface MockInboxItem {
  id: string;
  source: string;
  kind: string;
  account: string;
  account_name: string;
  title: string;
  preview: string;
  count: number;
  updated_at: string;
  tier: "action" | "fyi" | "noise";
  version: string;
}

export const DEFAULT_INBOX_ITEMS: MockInboxItem[] = [
  {
    id: "lark-1",
    source: "lark",
    kind: "dm",
    account: "feishu",
    account_name: "Lark",
    title: "Release checklist follow-up",
    preview: "Please confirm the UI polish PR before tonight.",
    count: 1,
    updated_at: AT,
    tier: "action",
    version: "v-lark-1",
  },
  {
    id: "mail-1",
    source: "mail",
    kind: "mail",
    account: "acc1",
    account_name: "me@example.com",
    title: "Weekly status summary",
    preview: "Split panel & overlay verification passed cleanly.",
    count: 1,
    updated_at: AT,
    tier: "action",
    version: "v-mail-1",
  },
];

export interface OpenAppOptions {
  theme?: "light" | "dark";
  surfaceStyle?: "default" | "liquid-glass";
  desktopBridge?: boolean;
  viewport?: { width: number; height: number };
  reducedMotion?: "reduce" | "no-preference";
  path?: string;
  inboxItems?: MockInboxItem[];
}

export type OpenAppFn = (options?: OpenAppOptions) => Promise<Page>;

export const test = base.extend<{ openApp: OpenAppFn }>({
  openApp: async ({ page }, use) => {
    const pageErrors: string[] = [];
    const consoleErrors: string[] = [];
    page.on("pageerror", (err) => pageErrors.push(String(err)));
    page.on("console", (msg) => {
      if (msg.type() === "error") consoleErrors.push(msg.text());
    });

    const openApp: OpenAppFn = async (options = {}) => {
      const {
        theme = "light",
        surfaceStyle = "default",
        desktopBridge = false,
        viewport,
        reducedMotion = "reduce",
        path = "/",
        inboxItems = DEFAULT_INBOX_ITEMS,
      } = options;

      if (viewport) await page.setViewportSize(viewport);
      await page.emulateMedia({ colorScheme: theme, reducedMotion });
      await page.clock.setFixedTime(new Date(AT));

      await page.addInitScript(
        ({ themeArg, surfaceArg, desktopArg }) => {
          localStorage.setItem("persona.token", "mock");
          localStorage.setItem("persona.language", "en");
          localStorage.setItem("persona.theme", themeArg);
          if (surfaceArg === "liquid-glass") {
            localStorage.setItem("persona.surface-style", "liquid-glass");
          } else {
            localStorage.removeItem("persona.surface-style");
          }
          if (desktopArg) {
            (window as { personaDesktop?: unknown }).personaDesktop = {
              version: 1,
              platform: "darwin",
              deviceName: "Demo Mac",
              notificationPermission: () => "granted",
              setBadge: () => {},
              notify: () => false,
              closeNotification: () => {},
              clearNotifications: () => {},
              onOpenNotification: () => () => {},
              desktopPresence: () => ({ active: true, notifies: true }),
              onPresenceChange: () => () => {},
              setLaunchAtLogin: async (enabled: boolean) => ({ enabled }),
              getLaunchAtLogin: async () => ({ enabled: false }),
              windowChrome: () => ({ unifiedTitlebar: true, fullScreen: false, focused: true }),
              onWindowChromeChange: () => () => {},
              setSurfaceStyle: () => {},
              setThemeSource: () => {},
            };
          } else {
            delete (window as { personaDesktop?: unknown }).personaDesktop;
          }
        },
        { themeArg: theme, surfaceArg: surfaceStyle, desktopArg: desktopBridge },
      );

      await page.route(/^https?:\/\/(?!127\.0\.0\.1|localhost|\[::1\])/, (route) => route.abort());
      await page.route("**/api/v1/**", (route) => {
        const reqUrl = new URL(route.request().url());
        const apiPath = reqUrl.pathname.replace(/^.*\/api\/v1/, "");
        if (/\/file(?:\/|$)/.test(apiPath)) {
          return route.fulfill({ status: 200, contentType: "image/png", body: PNG_1X1 });
        }
        if (route.request().method() !== "GET") {
          return route.fulfill({ status: 200, contentType: "application/json", body: "{}" });
        }
        let data: unknown = {};
        if (apiPath === "/machines") data = { machines: [MACHINE] };
        else if (apiPath === "/sessions") data = { sessions: [MAIN_SESSION, AGENT_SESSION] };
        else if (apiPath === "/interactions") data = { interactions: [] };
        else if (apiPath === "/assistant") {
          data = { machine_id: MACHINE.id, main: MAIN_ID, conversations: [MAIN_SESSION], tasks: [], memory: [] };
        } else if (apiPath.endsWith("/feed")) {
          data = { items: [], plans: {}, task_cards: [] };
        } else if (apiPath === "/todos") {
          data = { todos: [] };
        } else if (apiPath === "/extensions/ui") {
          data = { slots: [] };
        } else if (apiPath === "/extensions") {
          data = { extensions: [] };
        } else if (apiPath === "/managers") {
          data = { managers: [] };
        } else if (apiPath === "/mail/accounts") {
          data = { accounts: [{ id: "acc1", email: "me@example.com" }] };
        } else if (apiPath.startsWith("/inbox/done/days")) {
          data = { days: [] };
        } else if (apiPath.startsWith("/inbox")) {
          data = { items: inboxItems };
        } else if (apiPath === `/sessions/${MACHINE.id}/${MAIN_ID}/items`) {
          data = {
            items: [
              {
                id: "u1",
                seq: 1,
                role: "user",
                kind: "text",
                text: "<attached_images>\n/demo/shot.png\n</attached_images>\nCheck image viewer backdrop",
                ts: AT,
              },
              {
                id: "a1",
                seq: 2,
                role: "assistant",
                kind: "text",
                text: "Ready to inspect split panel and overlays.",
                ts: AT,
              },
            ],
            has_more: false,
          };
        } else if (apiPath === `/sessions/${MACHINE.id}/${AGENT_ID}/items`) {
          data = {
            items: [
              { id: "su1", seq: 1, role: "user", kind: "text", text: "Run split panel check", ts: AT },
              { id: "sa1", seq: 2, role: "assistant", kind: "text", text: "Split panel active.", ts: AT },
            ],
            has_more: false,
          };
        } else if (apiPath === `/sessions/${MACHINE.id}/${MAIN_ID}`) {
          data = { session: MAIN_SESSION };
        } else if (apiPath === `/sessions/${MACHINE.id}/${AGENT_ID}`) {
          data = { session: AGENT_SESSION };
        }
        return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(data) });
      });

      await page.routeWebSocket("**/api/v1/ws**", (ws) => {
        ws.send(JSON.stringify({ type: "hello", server_version: "demo", hub_name: "Demo hub", features: [] }));
        ws.onMessage((msg) => {
          if (String(msg).includes("app_ping")) ws.send(JSON.stringify({ type: "pong" }));
        });
      });

      await page.goto(path);
      await page.locator("[data-app-shell] > main header").first().waitFor({ state: "visible" });
      await page.evaluate(() => document.fonts.ready);
      return page;
    };

    await use(openApp);
    expect(pageErrors, "Unexpected pageerror during UI test").toEqual([]);
    expect(consoleErrors, "Unexpected console.error during UI test").toEqual([]);
  },
});

export async function openSplitPanelAgent(page: Page): Promise<void> {
  const inProgress = page.locator('[data-tray-entry="running"]').or(page.getByRole("button", { name: /In progress/ })).first();
  await inProgress.waitFor({ state: "visible" });
  if ((await inProgress.getAttribute("aria-expanded")) !== "true") {
    await inProgress.click();
  }
  const agentRow = page.locator(`[data-running-agent="${MACHINE.id}/${AGENT_ID}"]`);
  await agentRow.waitFor({ state: "visible" });
  await agentRow.click();
  await page.locator("aside[data-side-panel] header").waitFor({ state: "visible" });
  await page.locator("aside[data-side-panel] textarea").waitFor({ state: "visible" });
}

export { expect };
