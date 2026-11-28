/// <reference types="node" />
// Settings draws the Extensions and Connectors pages from chunks of their own, loaded when one is opened or its sidebar
// row is pointed at or focused (Settings.tsx, lazyPage). What only those pages draw (the market, an extension's sign-in
// and settings, the hub's connectors, the Lark bot, the sent-message rules) stays with them: a static import of any of
// it from another module would put it back into the Settings chunk, which every Settings page loads.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { describe, expect, it } from "vitest";

const root = new URL("../../", import.meta.url);
// each module, and the modules allowed to import it statically (admin/ is an entry of its own)
const pages: Record<string, string[]> = {
  Extensions: ["views/settings/IntegrationDetail.tsx"],
  Connectors: ["views/settings/IntegrationDetail.tsx"],
  IntegrationDetail: [],
  Integrations: [],
  ExtensionMarket: ["views/settings/Extensions.tsx", "views/settings/IntegrationDetail.tsx", "admin/pages/Extensions.tsx"],
  ExtensionAuth: ["views/settings/Extensions.tsx", "views/settings/IntegrationDetail.tsx"],
  ExtensionSettingsPage: ["views/settings/Extensions.tsx", "views/settings/IntegrationDetail.tsx"],
  ExtensionOperation: ["views/settings/ExtensionMarket.tsx"],
  HubConnector: [
    "views/settings/Extensions.tsx",
    "views/settings/Connectors.tsx",
    "views/settings/FeishuBot.tsx",
    "views/settings/integrations/ConnectorSection.tsx",
  ],
  FeishuBot: ["views/settings/Connectors.tsx", "views/settings/integrations/FeishuBotSection.tsx"],
  SentSettings: ["views/settings/Connectors.tsx", "views/settings/Integrations.tsx"],
  SentChatRules: ["views/settings/SentSettings.tsx"],
};

function sources(dir: URL): string[] {
  return readdirSync(dir).flatMap((name) => {
    const u = new URL(name, dir);
    if (statSync(u).isDirectory()) return sources(new URL(name + "/", dir));
    return /\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name) ? [u.pathname] : [];
  });
}

describe("the Extensions and Connectors pages boundary", () => {
  it("imports what only those pages draw from those pages alone", () => {
    const base = new URL(root).pathname;
    for (const file of sources(root)) {
      const rel = file.slice(base.length);
      const text = readFileSync(file, "utf8");
      for (const [name, allowed] of Object.entries(pages)) {
        if (allowed.includes(rel)) continue;
        const staticImport = new RegExp(`^import\\s+(?!type\\b)[^;]*from\\s+["'](?:\\.{1,2}/)+(?:views/)?(?:settings/)?${name}["']`, "m");
        expect(text, `${rel} imports ${name}`).not.toMatch(staticImport);
      }
    }
  });

  it("draws both pages in Settings through lazyPage", () => {
    const settings = readFileSync(new URL("../Settings.tsx", import.meta.url), "utf8");
    expect(settings).toMatch(/const Extensions = lazyPage\(\(\) => import\("\.\/settings\/Extensions"\)\.then\(\(m\) => m\.Extensions\)\)/);
    expect(settings).toMatch(/const Connectors = lazyPage\(\(\) => import\("\.\/settings\/Connectors"\)\.then\(\(m\) => m\.Connectors\)\)/);
    expect(settings).toMatch(/const Integrations = lazyPage\(\(\) => import\("\.\/settings\/Integrations"\)\.then\(\(m\) => m\.Integrations\)\)/);
    // the pages render through these in Settings.navigation / Settings.groups tests (loadPages)
  });

  it("lazily loads SourcesUI in Settings and Integrations without statically importing SourcesUI or SourcesGraph", () => {
    const settings = readFileSync(new URL("../Settings.tsx", import.meta.url), "utf8");
    const integrations = readFileSync(new URL("./Integrations.tsx", import.meta.url), "utf8");
    const detail = readFileSync(new URL("./IntegrationDetail.tsx", import.meta.url), "utf8");
    const staticSources = /^import\s+(?!type\b)[^;]*from\s+["'][^"']*subscriptions\/Sources(?:UI|Graph)["']/m;
    expect(settings).not.toMatch(staticSources);
    expect(integrations).not.toMatch(staticSources);
    expect(detail).not.toMatch(staticSources);
    expect(settings).toMatch(/lazyPage\(\(\) => import\("\.\/subscriptions\/SourcesUI"\)\.then\(\(m\) => m\.SourcesUI\)\)/);
    expect(integrations).toMatch(/lazyPage\(\(\) => import\("\.\.\/subscriptions\/SourcesUI"\)\.then\(\(m\) => m\.SourcesUI\)\)/);
  });

  it("keeps Labs and its forms off the sidebar's import path", () => {
    const settings = readFileSync(new URL("../Settings.tsx", import.meta.url), "utf8");
    expect(settings).toMatch(/const LabsSettings = lazyPage\(\(\) => import\("\.\/settings\/Labs"\)/);
    for (const path of ["views/SidebarParts.tsx", "lib/labs-catalog.ts", "lib/labs-seen.ts"]) {
      const source = readFileSync(new URL(path, root), "utf8");
      expect(source, path).not.toMatch(/^import\s+(?!type\b)[^;]*from\s+["'][^"']*\/(?:Labs|ConfigurationForm|labs-seen-write|labs-(?:work-guide|terminal|todos-v2|integrations-v2))["']/m);
    }
  });
});
