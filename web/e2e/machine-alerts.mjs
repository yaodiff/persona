// The Machines page's alerts and what fixes them (#8037; docs/web/web.md "Machines"): light and dark screenshots at a
// desktop width (1280) and a phone's (390) of the cards with their alert chips, the memory sheet, the disk sheet with
// its cleanup confirmation, the version-mismatch sheet, the hand-off picker and the capability index; then, once, a
// Redeploy through its stepper. Checks along the way: every chip is a button that opens the sheet on its alert, no
// button's text wraps, nothing in a sheet is wider than the sheet, and the page logs no errors.
//
// Needs the web page against the fake hub's alert fleet, and Chrome or Chromium. Start both yourself:
//
//   MOCK_MACHINE_ALERTS=1 MOCK_MANAGERS=1 PORT=<port> python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort --host 127.0.0.1 &)
//   node web/e2e/machine-alerts.mjs http://127.0.0.1:<web port> <artifact directory> [--lang en|zh-Hans|ja] [--chromium <executable>]
//
// The run's last scene redeploys the mock's Studio Mac (restart the mock to run it again). Prints one line per check
// and a verdict; exits 1 on a failed check and 2 on its four-minute timeout.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name, fallback) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : fallback;
};
const base = (args[0] ?? "").replace(/\/$/, "");
const out = args[1];
const lang = flag("--lang", "en");
const executable = flag("--chromium", undefined);
if (!base || !out || out.startsWith("--")) {
  console.error("usage: node web/e2e/machine-alerts.mjs <web URL> <artifact directory> [--lang en|zh-Hans|ja] [--chromium <executable>]");
  process.exit(2);
}
const origin = new URL(base);
if (!["127.0.0.1", "localhost", "[::1]"].includes(origin.hostname)) {
  console.error("a loopback web server backed by the fake hub is required");
  process.exit(2);
}
mkdirSync(out, { recursive: true });
const watchdog = setTimeout(() => { console.log("VERDICT TIMEOUT machine alerts"); process.exit(2); }, 240_000);

const failures = [];
const check = (ok, what, detail = "") => {
  console.log(`${ok ? "ok  " : "FAIL"} ${what}${detail ? `: ${detail}` : ""}`);
  if (!ok) failures.push(what);
};

// the fake hub's alert fleet (scripts/mock/machine_alerts.py)
const FLEET = ["Garage NAS", "Travel Laptop", "Build Box", "Cloud Runner", "Studio Mac", "Office Mini"];

const browser = await chromium.launch(executable ? { executablePath: executable, headless: true, args: ["--no-sandbox"] } : { channel: "chrome", headless: true });

async function open(theme, width, view = "cards") {
  const height = width < 600 ? 844 : 860;
  const context = await browser.newContext({ viewport: { width, height }, deviceScaleFactor: width < 600 ? 2 : 1, hasTouch: width < 600 });
  await context.addInitScript(([t, l, v]) => {
    localStorage.setItem("persona.theme", t);
    localStorage.setItem("persona.language", l);
    localStorage.setItem("persona.machines.view", v);
  }, [theme, lang, view]);
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.goto(`${base}/settings?token=mock`);
  await page.waitForSelector("aside, nav", { timeout: 20_000 });
  await page.goto(`${base}/machines`);
  return { context, page, errors };
}

/** A card by the computer's name. */
const card = (page, name) => page.locator("section").filter({ has: page.locator("h2", { hasText: name }) }).first();

/** Buttons whose text runs over one line, and things wider than the open sheet. */
async function layoutProblems(page, scope = "body") {
  return page.evaluate((sel) => {
    const root = document.querySelector(sel) ?? document.body;
    const wrapped = [...root.querySelectorAll("button")].filter((b) => {
      if (!b.offsetParent || !b.textContent?.trim()) return false;
      const lh = parseFloat(getComputedStyle(b).lineHeight) || 16;
      const range = document.createRange();
      range.selectNodeContents(b);
      return range.getClientRects().length > 1 && range.getBoundingClientRect().height > lh * 1.5;
    }).map((b) => b.textContent.trim());
    const box = root.getBoundingClientRect();
    const wide = [...root.querySelectorAll("*")].filter((e) => {
      const r = e.getBoundingClientRect();
      return r.width > 0 && r.right > box.right + 1 && getComputedStyle(e).position !== "fixed";
    }).slice(0, 3).map((e) => `${e.tagName.toLowerCase()}.${String(e.className).slice(0, 40)}`);
    return { wrapped, wide };
  }, scope);
}

async function shot(page, name) {
  await page.waitForTimeout(350);
  await page.screenshot({ path: join(out, `${name}.png`) });
}

async function sheetFor(page, machine, alert) {
  await card(page, machine).locator(`[data-alert=${alert}]`).click();
  await page.waitForSelector(`[data-testid=alert-sheet][data-kind=${alert}]`, { timeout: 10_000 });
}

async function checkSheet(page, label) {
  const p = await layoutProblems(page, "[role=dialog]");
  check(p.wrapped.length === 0, `${label}: no button text wraps`, p.wrapped.join(" | "));
  check(p.wide.length === 0, `${label}: nothing is wider than the sheet`, p.wide.join(" | "));
}

try {
  for (const theme of ["light", "dark"]) {
    for (const width of [1280, 390]) {
      const tag = `${width}-${theme}`;
      // 1. the cards and their chips
      {
        const { context, page, errors } = await open(theme, width);
        await page.waitForSelector("[data-testid=machine-alerts] [data-alert]", { timeout: 20_000 });
        await page.waitForTimeout(800);
        const chips = await page.$$eval("[data-testid=machine-alerts] [data-alert]", (b) => b.map((x) => ({ tag: x.tagName, alert: x.dataset.alert, severity: x.dataset.severity, label: x.getAttribute("aria-label") })));
        check(chips.length >= 4 && chips.every((c) => c.tag === "BUTTON" && c.label), `${tag}: every alert chip is a labelled button`, chips.map((c) => `${c.alert}/${c.severity}`).join(" "));
        const order = (await page.$$eval("section h2", (h) => h.map((x) => x.textContent))).filter((n) => FLEET.includes(n));
        check(order[0] === "Garage NAS", `${tag}: the offline computer comes first`, order.join(" › "));
        check(order.indexOf("Travel Laptop") < order.indexOf("Build Box") && order.indexOf("Office Mini") === order.length - 1, `${tag}: critical before warning, the healthy one last`, order.join(" › "));
        const p = await layoutProblems(page, "main");
        check(p.wrapped.length === 0, `${tag}: no button text wraps on the cards`, p.wrapped.join(" | "));
        await shot(page, `cards-${tag}`);
        if (width === 390) {
          await card(page, "Travel Laptop").scrollIntoViewIfNeeded();
          await shot(page, `cards-scrolled-${tag}`);
        }
        check(errors.length === 0, `${tag}: no page errors on the cards`, errors.join("; "));
        await context.close();
      }
      // 2. the memory sheet, then 5. its hand-off picker
      {
        const { context, page, errors } = await open(theme, width);
        await page.waitForSelector("[data-alert=memory]", { timeout: 20_000 });
        await sheetFor(page, "Travel Laptop", "memory");
        await page.waitForSelector("[data-testid=process-groups]", { timeout: 10_000 });
        await page.waitForSelector("[data-testid=agent-groups]", { timeout: 10_000 });
        check(await page.locator("[data-testid=process-groups] [data-process]").count() > 0, `${tag}: the memory sheet lists process groups`);
        await checkSheet(page, `${tag} memory sheet`);
        await shot(page, `memory-sheet-${tag}`);
        await page.locator("[data-testid=stop-idle]").click();
        await page.waitForSelector("[data-testid=stop-list]");
        await shot(page, `memory-stop-confirm-${tag}`);
        await page.keyboard.press("Escape");
        await page.locator("[data-testid=handoff]").click();
        await page.waitForSelector("[data-testid=handoff-targets] [data-target]", { timeout: 10_000 });
        await page.waitForTimeout(900); // the computers' tools come in
        const targets = await page.$$eval("[data-testid=handoff-targets] [data-target]", (rows) => rows.map((r) => ({ id: r.dataset.target, eligible: r.dataset.eligible, reason: r.textContent })));
        check(targets.some((r) => r.id === "nas" && r.eligible === "false"), `${tag}: the offline computer is not a target`, targets.map((r) => `${r.id}:${r.eligible}`).join(" "));
        check(targets[0]?.eligible === "true", `${tag}: an eligible computer is ranked first`, targets[0]?.reason);
        await checkSheet(page, `${tag} hand-off`);
        await shot(page, `handoff-${tag}`);
        check(errors.length === 0, `${tag}: no page errors in the memory sheet`, errors.join("; "));
        await context.close();
      }
      // 3. Disk recovery on a computer that predates usage maps.
      {
        const { context, page, errors } = await open(theme, width);
        await page.waitForSelector("[data-alert=disk]", { timeout: 20_000 });
        await page.route("**/storage/usage?*", route => route.fulfill({ status: 409, json: { code: "node_outdated" } }));
        await card(page, "Build Box").locator("[data-alert=disk]").click();
        await page.waitForURL(u => u.pathname.endsWith("/storage") && u.searchParams.get("mode") === "reclaim");
        await page.waitForSelector("[data-testid=caches] [data-cache]", { timeout: 10_000 });
        const inUse = await page.$eval("[data-cache=go-build] input", (i) => i.disabled).catch(() => null);
        check(inUse === true, `${tag}: the cache in use can't be picked`);
        await page.getByTestId("usage-map").scrollIntoViewIfNeeded();
        await shot(page, `storage-${tag}`);
        await page.locator("[data-testid=caches]").scrollIntoViewIfNeeded();
        await shot(page, `disk-caches-${tag}`);
        await page.locator("[data-testid=clean-selected]").click();
        await page.waitForSelector("[data-testid=clean-list]");
        await shot(page, `disk-clean-confirm-${tag}`);
        check(errors.length === 0, `${tag}: no page errors in the disk sheet`, errors.join("; "));
        await context.close();
      }
      // 4. the version mismatch sheet, from the strip's Details
      {
        const { context, page, errors } = await open(theme, width);
        await card(page, "Studio Mac").locator("[data-testid=deploy-details]").click({ timeout: 20_000 });
        await page.waitForSelector("[data-testid=alert-sheet][data-kind=mismatch] [data-testid=builds]", { timeout: 10_000 });
        const gap = await page.textContent("[data-testid=build-gap]");
        check(/older|早|古い/.test(gap ?? ""), `${tag}: the sheet says how much older the node's build is`, gap);
        await checkSheet(page, `${tag} mismatch sheet`);
        await shot(page, `mismatch-sheet-${tag}`);
        check(errors.length === 0, `${tag}: no page errors in the mismatch sheet`, errors.join("; "));
        await context.close();
      }
      // the offline and launcher sheets, for a look
      {
        const { context, page, errors } = await open(theme, width);
        await page.waitForSelector("[data-alert=offline]", { timeout: 20_000 });
        await sheetFor(page, "Garage NAS", "offline");
        check(await page.locator("[data-testid=offline-checklist] li").count() >= 3, `${tag}: the offline sheet has a numbered checklist`);
        await checkSheet(page, `${tag} offline sheet`);
        await shot(page, `offline-sheet-${tag}`);
        await page.keyboard.press("Escape");
        await page.waitForTimeout(300);
        await sheetFor(page, "Cloud Runner", "launcher");
        await page.waitForSelector("[data-testid=healthy-launchers]", { timeout: 10_000 });
        await checkSheet(page, `${tag} launcher sheet`);
        await shot(page, `launcher-sheet-${tag}`);
        check(errors.length === 0, `${tag}: no page errors in the offline and launcher sheets`, errors.join("; "));
        await context.close();
      }
      // 6. the capability index
      {
        const { context, page, errors } = await open(theme, width, "capabilities");
        await page.waitForSelector("[data-testid=capabilities] [data-row]", { timeout: 20_000 });
        await page.waitForFunction(() => !document.querySelector("[data-testid=capabilities] [aria-busy=true]"), null, { timeout: 15_000 }).catch(() => {});
        const rows = await page.locator("[data-testid=capabilities] [data-row]").count();
        check(rows >= 6, `${tag}: one capability row per computer`, String(rows));
        const p = await layoutProblems(page, "[data-testid=capabilities]");
        check(p.wrapped.length === 0, `${tag}: no button text wraps in the index`, p.wrapped.join(" | "));
        await shot(page, `capabilities-${tag}`);
        check(errors.length === 0, `${tag}: no page errors in the index`, errors.join("; "));
        await context.close();
      }
    }
  }
  // The capability index as a table, where the page is wide enough.
  for (const theme of ["light", "dark"]) {
    const { context, page, errors } = await open(theme, 1600, "capabilities");
    await page.waitForSelector("[data-testid=capabilities] [data-row]", { timeout: 20_000 });
    await page.waitForFunction(() => !document.querySelector("[data-testid=capabilities] [aria-busy=true]"), null, { timeout: 15_000 }).catch(() => {});
    const head = await page.$eval("[data-testid=capabilities] [role=row]", (r) => getComputedStyle(r).display);
    check(head === "grid", `1600-${theme}: the index is a table`, head);
    const p = await layoutProblems(page, "[data-testid=capabilities]");
    check(p.wide.length === 0, `1600-${theme}: no column runs out of the table`, p.wide.join(" | "));
    await shot(page, `capabilities-1600-${theme}`);
    check(errors.length === 0, `1600-${theme}: no page errors in the index`, errors.join("; "));
    await context.close();
  }
  // Once: Redeploy through its stepper (this changes the mock's Studio Mac).
  {
    const { context, page, errors } = await open("light", 1280);
    await card(page, "Studio Mac").locator("[data-testid=deploy-details]").click({ timeout: 20_000 });
    await page.locator("[data-testid=redeploy]").click();
    await page.waitForSelector("[data-testid=redeploy-confirm]");
    await shot(page, "mismatch-confirm-1280-light");
    await page.locator("[data-testid=redeploy-confirm]").click();
    await page.waitForSelector("[data-testid=deploy-stepper] [data-step=transferring][data-state=current]", { timeout: 10_000 }).catch(() => {});
    await shot(page, "mismatch-stepper-1280-light");
    await page.waitForSelector("[data-testid=deploy-done]", { timeout: 20_000 }).catch(() => {});
    check(await page.locator("[data-testid=deploy-done]").count() === 1, "the stepper ends on the node's new build");
    await shot(page, "mismatch-done-1280-light");
    check(errors.length === 0, "no page errors while redeploying", errors.join("; "));
    await context.close();
  }
} catch (e) {
  check(false, "the run finished", String(e?.stack ?? e));
} finally {
  await browser.close();
  clearTimeout(watchdog);
}
console.log(failures.length ? `VERDICT FAIL machine alerts (${failures.length}): ${failures.join("; ")}` : "VERDICT PASS machine alerts");
process.exit(failures.length ? 1 : 0);
