// Unread Persona chats under Pinned and Recent in the sidebar (#13425), measured in Chrome against the mock hub:
//
//   MOCK_LEADERS=1 MOCK_RECENT=1 PORT=<mock port> python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:<mock port> npx vite --host 127.0.0.1 --port <web port> --strictPort &)
//   node web/e2e/sidebar-unread-chats.mjs <web origin> <mock hub origin> <artifact directory> [Chromium executable]
//
// Wide (1440) and phone (390) widths, standard and Large text, light and dark, in English. A reply lands in two pinned leads (one with Agents at
// work under it, whose arc its own Unread replaces) and two Recent chats through the mock's GET /mock/incoming (a new
// turn, pushed as the hub pushes it). Each row is measured before and after: its height, where its mark's slot, its
// title and its time sit in it stay; the slot draws the unread mark in place of the chat icon and the title takes the
// unread weight. Folded, Recent's heading and then the Persona chats heading count the Unread chats they hide; open,
// neither does. Opening one clears its mark at once and it stays clear; a later reply brings it back. Screenshots and
// results.json go to the artifact directory; a four-minute watchdog ends the run, and the last line is the verdict.
import assert from "node:assert/strict";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [base, hub, output, executablePath] = process.argv.slice(2);
const loopback = /^http:\/\/(127\.0\.0\.1|localhost):\d+$/;
if (!loopback.test(base ?? "") || !loopback.test(hub ?? "") || !output) {
  console.error("usage: node web/e2e/sidebar-unread-chats.mjs <web origin> <mock hub origin> <artifact directory> [Chromium executable]");
  process.exit(2);
}
mkdirSync(output, { recursive: true });
const watchdog = setTimeout(() => {
  console.log("TIMEOUT sidebar unread chats: no verdict within four minutes");
  process.exit(3);
}, 240_000);

const auth = { Authorization: "Bearer mock" };
const hubGet = async (path) => {
  const res = await fetch(`${hub}${path}`, { headers: auth });
  assert.equal(res.status, 200, `${path}: ${res.status}`);
  return res.json();
};
const assistant = await hubGet("/api/v1/assistant");
const tree = await hubGet("/api/v1/assistant/directions/tree");
const machine = assistant.machine_id ?? assistant.conversations[0].machine_id;
const leaders = assistant.leaders ?? [];
const runs = new Set((tree.directions ?? []).filter((d) => d.running && !d.secretary).map((d) => d.id));
// a lead with Agents at work under it (the arc at rest) and one without
const busyLead = leaders.find((id) => runs.has(id));
const quietLead = leaders.find((id) => !runs.has(id));
assert(busyLead && quietLead, "MOCK_LEADERS=1: a lead with something running under it and one without");
const ownChats = assistant.conversations.filter((c) => c.session_id !== assistant.main && !leaders.includes(c.session_id) && !c.archived && c.origin === "user");
assert(ownChats.length >= 3, "MOCK_RECENT=1: the user's own chats under Recent");
const [recentA, recentB] = ownChats.slice(0, 2).map((c) => c.session_id);
const targets = [busyLead, quietLead, recentA, recentB];
const reply = (sid, text) => hubGet(`/mock/incoming?${new URLSearchParams({ mid: machine, sid, text })}`);

const results = { base, cases: [] };
const browser = await chromium.launch(executablePath ? { executablePath } : { channel: "chrome" });
let failed = false;
try {
  for (const [phone, textSize] of [[false, "standard"], [true, "standard"], [false, "large"], [true, "large"]]) {
    for (const theme of ["light", "dark"]) {
      const name = `${phone ? "phone" : "wide"}-${theme}${textSize === "large" ? "-large" : ""}`;
      const context = await browser.newContext({
        viewport: { width: phone ? 390 : 1440, height: phone ? 844 : 1000 },
        hasTouch: phone, isMobile: phone, colorScheme: theme, reducedMotion: "reduce",
      });
      try {
        await context.addInitScript(({ theme, textSize }) => {
          localStorage.setItem("persona.language", "en");
          localStorage.setItem("persona.theme", theme);
          if (textSize === "large") localStorage.setItem("persona.text-size", textSize);
        }, { theme, textSize });
        const page = await context.newPage();
        page.setDefaultTimeout(12_000);
        const errors = [];
        page.on("pageerror", (error) => errors.push(String(error)));
        await page.goto(`${base}/overview?token=mock`);
        const sidebar = page.locator("aside");
        // a phone's sidebar is a drawer, off the screen's left edge when closed (a row or a page closes it); its button
        // says what waits on you after its name ("Sidebar, 1 needs you")
        const showSidebar = async () => {
          if (!phone) return;
          await page.waitForFunction(() => (document.querySelector("aside")?.getBoundingClientRect().right ?? 1) <= 0);
          await page.getByRole("button", { name: /^Sidebar(,|$)/ }).click();
          await page.waitForFunction(() => document.querySelector("aside").getBoundingClientRect().left >= 0);
        };
        await showSidebar();
        const persona = sidebar.getByRole("button", { name: "Persona chats", exact: true });
        const recent = sidebar.getByRole("button", { name: "Recent", exact: true });
        const personaTally = persona.locator("xpath=..").locator('[data-testid="state-tally"]');
        const recentTally = recent.locator('xpath=ancestor::*[@role="heading"][1]/..').locator('[data-testid="state-tally"]');
        const row = (sid) => sidebar.locator(`a[href*="/persona/${sid}"]`).first();
        // the only state mark a Persona chat's row draws is in the slot at its start
        const unreadMark = (sid) => row(sid).locator('[data-state="unread"][data-shape="glow"]');
        // every chat starts read (a previous case left replies unread), pushed as the hub pushes a change
        for (const sid of targets) await hubGet(`/mock/session_state?${new URLSearchParams({ mid: machine, sid, state: "idle" })}`);
        for (const sid of targets) await row(sid).waitFor();
        await page.waitForFunction((ids) => ids.every((id) => !document.querySelector(`aside a[href*="/persona/${id}"] [data-state="unread"]`)), targets);
        // the direction tree read: the busy lead carries the working arc
        await row(busyLead).locator('[data-state="working"]').waitFor();
        await page.waitForTimeout(300);

        /** Where a row's parts sit, against the row's own box: what must not move when it turns unread. */
        const measure = (sid) => row(sid).evaluate((a) => {
          const box = (el) => el?.getBoundingClientRect();
          const r = box(a);
          const slot = box(a.firstElementChild);
          const title = a.querySelector("span.text-meta");
          const time = box(a.querySelector("[data-row-age]"));
          return {
            height: r.height, width: r.width,
            slot: { left: slot.left - r.left, top: slot.top - r.top, width: slot.width, height: slot.height },
            titleLeft: box(title).left - r.left, titleWeight: getComputedStyle(title).fontWeight, titleColor: getComputedStyle(title).color,
            timeRight: time ? r.right - time.right : null,
            mark: a.firstElementChild.querySelector("[data-state]")?.getAttribute("data-state") ?? "icon",
          };
        });
        const before = Object.fromEntries(await Promise.all(targets.map(async (sid) => [sid, await measure(sid)])));
        assert.equal(before[busyLead].mark, "working", "a lead with Agents at work under it carries the arc");
        for (const sid of [quietLead, recentA, recentB]) assert.equal(before[sid].mark, "icon", `${sid}: the chat icon at rest`);
        await sidebar.screenshot({ path: join(output, `read-${name}.png`), animations: "disabled" });

        // a reply nobody has opened, in two pinned leads and two Recent chats
        for (const sid of targets) await reply(sid, "Here is what I found.");
        for (const sid of targets) await unreadMark(sid).waitFor();
        await page.waitForTimeout(300);
        const after = Object.fromEntries(await Promise.all(targets.map(async (sid) => [sid, await measure(sid)])));
        for (const sid of targets) {
          const [b, a] = [before[sid], after[sid]];
          assert.equal(a.mark, "unread", `${sid}: the unread mark (its own Unread wins over the lead's arc)`);
          assert.equal(a.height, b.height, `${sid}: row height`);
          assert.deepEqual(a.slot, b.slot, `${sid}: the mark's slot`);
          assert.equal(a.titleLeft, b.titleLeft, `${sid}: the title's place`);
          assert.equal(a.timeRight, b.timeRight, `${sid}: the time at the row's end`);
          assert.equal(b.titleWeight, "400", `${sid}: a read title`);
          assert.equal(a.titleWeight, "500", `${sid}: the unread weight`);
        }
        // a chat nobody replied to keeps its chat icon
        const quiet = ownChats[2].session_id;
        assert.equal((await measure(quiet)).mark, "icon");
        await sidebar.screenshot({ path: join(output, `unread-${name}.png`), animations: "disabled" });

        // open: neither heading counts; folded, each counts the Unread chats it hides
        assert.equal(await personaTally.count(), 0, "open Persona chats: no tally");
        assert.equal(await recentTally.count(), 0, "open Recent: no tally");
        await recent.click();
        assert.equal(await recent.getAttribute("aria-expanded"), "false");
        await recentTally.waitFor();
        const tallyNumber = (tally) => tally.locator(':scope > [data-state="unread"]').textContent();
        assert.equal(await tallyNumber(recentTally), "2", "folded Recent counts its two");
        assert.equal(await recentTally.getAttribute("title"), "Folded chats: 2 unread");
        assert.equal(await personaTally.count(), 0, "Persona chats is open: no tally");
        await sidebar.screenshot({ path: join(output, `recent-folded-${name}.png`), animations: "disabled" });
        await persona.click();
        await personaTally.waitFor();
        assert.equal(await tallyNumber(personaTally), "4", "folded Persona chats counts Pinned and Recent");
        assert.equal(await personaTally.getAttribute("title"), "Folded chats: 4 unread");
        const heights = await page.evaluate(() => [...document.querySelectorAll("aside .sidebar-heading")].map((el) => el.getBoundingClientRect().height));
        await sidebar.screenshot({ path: join(output, `persona-folded-${name}.png`), animations: "disabled" });
        await persona.click();
        await recent.click();
        await unreadMark(recentA).waitFor();
        assert.equal(await personaTally.count() + await recentTally.count(), 0, "open again: no tally");

        // opening a chat clears its mark at once, and it stays clear after the hub has it
        const clicked = Date.now();
        await row(recentA).click();
        await page.waitForURL((url) => url.pathname.includes(recentA));
        await page.waitForFunction((id) => !document.querySelector(`aside a[href*="/persona/${id}"] [data-state="unread"]`), recentA, { timeout: 1_000 });
        const clearedMs = Date.now() - clicked;
        await page.waitForTimeout(2_500);
        await showSidebar();
        assert.equal((await measure(recentA)).mark, "icon", "seen: the chat icon again");
        // closed, it reads as a read chat (open, its row is the current one, medium as every current row), and a later
        // reply is a new turn nobody has opened. Just after the URL changed, the row was sometimes still read with the
        // current row's style (3 of 24 cases while this check was written; the cause was not found): the row's selection
        // changes are logged into results.json, and it is checked once it has settled
        await row(recentA).evaluate((a) => {
          const log = (window.__rowLog = []);
          const note = () => log.push([Math.round(performance.now()), a.isConnected, a.getAttribute("aria-current"), a.querySelector("span.text-meta")?.className.includes("font-medium") ?? null]);
          note();
          new MutationObserver(note).observe(a, { attributes: true, subtree: true, attributeFilter: ["aria-current", "class"] });
        });
        await sidebar.locator('a[href="/overview"]').first().click();
        await page.waitForURL((url) => url.pathname === "/overview");
        await showSidebar();
        await page.waitForFunction((id) => {
          const a = document.querySelector(`aside a[href*="/persona/${id}"]`);
          const title = a?.querySelector("span.text-meta");
          return !!title && !a.hasAttribute("aria-current") && getComputedStyle(title).fontWeight === "400";
        }, recentA, { timeout: 3_000 });
        const closed = await measure(recentA);
        const selection = await page.evaluate(() => window.__rowLog);
        assert.equal(closed.mark, "icon", "seen and closed: the chat icon");
        assert.equal(closed.titleWeight, "400", "seen and closed: a read title");
        await reply(recentA, "One more thing.");
        await unreadMark(recentA).waitFor();

        assert.deepEqual(errors, []);
        results.cases.push({ name, before, after, clearedMs, headingHeights: heights, selection, pass: true });
        console.log(`PASS ${name}: marks, rows unmoved, folded tallies 2 and 4, cleared in ${clearedMs} ms, back with a later reply`);
      } catch (error) {
        failed = true;
        results.cases.push({ name, pass: false, error: String(error?.stack ?? error) });
        console.log(`FAIL ${name}: ${error?.message ?? error}`);
      } finally {
        await context.close();
      }
    }
  }
} finally {
  await browser.close();
  writeFileSync(join(output, "results.json"), JSON.stringify(results, null, 2));
  clearTimeout(watchdog);
}
console.log(failed ? "FAIL sidebar unread chats" : "PASS sidebar unread chats: wide and phone, standard and Large text, light and dark");
process.exit(failed ? 1 : 0);
