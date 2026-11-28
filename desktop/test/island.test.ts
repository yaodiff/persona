import assert from "node:assert/strict";
import test from "node:test";
import { ISLAND_ROWS, ISLAND_SIDE, ISLAND_WINDOW, LIST_WIDTH, NOTCH_WIDTH, ROW_HEIGHT, hasNotch, islandDisplay, islandRect, islandSides, islandState } from "../src/island-model.js";
import { ISLAND_URL, islandFile } from "../src/island-page.js";

const row = (n: number, extra: Record<string, unknown> = {}) => ({ title: `Example agent ${n}`, state: "working", label: "Working", line: "", route: `/s/computer/session-${n}`, ...extra });

test("the island takes only a well-formed state from the web", () => {
  assert.equal(islandState(undefined), undefined);
  assert.equal(islandState("3"), undefined);
  assert.equal(islandState([]), undefined);
  const s = islandState({
    needsYou: 2, working: -1, unread: 1.5, summary: "  2 need you\n",
    rows: [
      row(1, { state: "needs_you", title: "  A   question \n", line: "Has a question: which framework?" }),
      row(2, { route: "https://other.example/s/computer/x" }),
      row(3, { route: "//other.example/x" }),
      row(4, { state: "idle" }),
      row(5, { title: " " }),
      "not a row",
      row(6), row(7), row(8), row(9), row(10), row(11),
    ],
  });
  assert.ok(s);
  assert.deepEqual([s.needsYou, s.working, s.unread, s.summary], [2, 0, 0, "2 need you"]);
  assert.equal(s.rows.length, ISLAND_ROWS);
  assert.deepEqual(s.rows[0], { title: "A question", state: "needs_you", label: "Working", line: "Has a question: which framework?", route: "/s/computer/session-1" });
  assert.deepEqual(s.rows.map(r => r.route), ["/s/computer/session-1", "/s/computer/session-6", "/s/computer/session-7", "/s/computer/session-8", "/s/computer/session-9"]);
});

test("the pill shows the most urgent category on the left and the next one on the right, as the iPhone's preset A", () => {
  const sides = (needsYou: number, working: number, unread: number) =>
    islandSides({ needsYou, working, unread, rows: [], summary: "" }).map(s => s && `${s.category}:${s.count}`);
  assert.deepEqual(sides(2, 3, 1), ["needs_you:2", "working:3"]);
  assert.deepEqual(sides(0, 3, 1), ["working:3", "unread:1"]);
  assert.deepEqual(sides(2, 0, 1), ["needs_you:2", "unread:1"]);
  assert.deepEqual(sides(0, 0, 4), ["unread:4", undefined]);
  assert.deepEqual(sides(0, 0, 0), [undefined, undefined]);
});

test("the island sits around the notch of the built-in display, or as a pill at the top center without one", () => {
  const macbook = { bounds: { x: 0, y: 0, width: 1512, height: 982 }, workArea: { x: 0, y: 38, width: 1512, height: 944 }, internal: true };
  const external = { bounds: { x: 1512, y: -200, width: 2560, height: 1440 }, workArea: { x: 1512, y: -175, width: 2560, height: 1415 }, internal: false };
  assert.equal(hasNotch(macbook), true);
  assert.equal(hasNotch(external), false);
  assert.equal(hasNotch({ ...macbook, workArea: { ...macbook.workArea, y: 24 } }), false); // a MacBook without a notch
  assert.equal(islandDisplay([external, macbook], external), macbook);
  assert.equal(islandDisplay([external], external), external);

  const closed = islandRect(macbook, false, 3);
  assert.deepEqual(closed, { x: Math.round((1512 - NOTCH_WIDTH - 2 * ISLAND_SIDE) / 2), y: 0, width: NOTCH_WIDTH + 2 * ISLAND_SIDE, height: 38 });
  const open = islandRect(macbook, true, 3);
  assert.deepEqual(open, { x: closed.x, y: 0, width: LIST_WIDTH, height: 38 + 3 * ROW_HEIGHT + 14 });

  const pill = islandRect(external, false, 0);
  assert.deepEqual(pill, { x: 1512 + Math.round((2560 - 2 * ISLAND_SIDE) / 2), y: -200, width: 2 * ISLAND_SIDE, height: 25 });
  const pillOpen = islandRect(external, true, 2);
  assert.deepEqual(pillOpen, { x: 1512 + Math.round((2560 - LIST_WIDTH) / 2), y: -200, width: LIST_WIDTH, height: 25 + 2 * ROW_HEIGHT + 14 });
});

test("opening the island around a notch only grows it downward: it covers no more of the menu bar than the pill (#16301)", () => {
  // built-in notched displays of three sizes, with menu bars of 33 to 38 pt
  for (const [width, height, bar] of [[1512, 982, 38], [1728, 1117, 33], [1470, 956, 37]] as const) {
    const d = { bounds: { x: 0, y: 0, width, height }, workArea: { x: 0, y: bar, width, height: height - bar }, internal: true };
    const closed = islandRect(d, false, 0);
    for (let rows = 1; rows <= ISLAND_ROWS; rows++) {
      const open = islandRect(d, true, rows);
      assert.deepEqual([open.x, open.y, open.width], [closed.x, closed.y, closed.width], `${width} pt, ${rows} rows`);
      assert.equal(open.height, bar + rows * ROW_HEIGHT + 14);
    }
  }
});

test("the island's window has no title bar style, so macOS leaves its corners square at the top of the screen (#16301)", () => {
  // Electron keeps a frameless window titled unless roundedCorners is false, and macOS rounds a titled window's corners
  assert.equal(ISLAND_WINDOW.frame, false);
  assert.equal(ISLAND_WINDOW.roundedCorners, false);
  assert.equal(ISLAND_WINDOW.transparent, true);
  assert.equal(ISLAND_WINDOW.hasShadow, false);
  assert.equal(ISLAND_WINDOW.focusable, false);
  assert.equal(ISLAND_WINDOW.type, "panel");
});

test("the island's scheme serves its page's three files and nothing else", () => {
  assert.equal(ISLAND_URL, "persona-island://app/island.html");
  for (const name of ["island.html", "island.css", "island.cjs"]) {
    const file = islandFile(`persona-island://app/${name}`);
    assert.ok(file?.startsWith("file://") && file.endsWith(`/island/${name}`), `${name}: ${file}`);
    assert.equal(islandFile(`persona-island://app/${name}`, "HEAD"), file);
  }
  assert.equal(islandFile(ISLAND_URL, "POST"), undefined);
  for (const raw of [
    "persona-island://app/island-preload.cjs", "persona-island://app/main.js", "persona-island://app/locales/en.json",
    "persona-island://app/", "persona-island://app/island/island.html", "persona-island://app/../main.js",
    "persona-island://app/%2e%2e/main.js", "persona-island://app/island%2Ehtml", "persona-island://app/..\\main.js",
    "persona-island://other/island.html", "persona-island://app:1/island.html", "persona-island://someone@app/island.html",
    "persona-island://app/island.html?x=1", "persona-island://app/island.html#top", "persona-setup://app/island.html",
    "file:///island.html", "https://app/island.html", "not a url",
  ]) assert.equal(islandFile(raw), undefined, raw);
});
