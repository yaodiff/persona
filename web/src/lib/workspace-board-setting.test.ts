// The workflow board's switch (#2475) takes only answers newer than what it shows (#4136): a read on its way when the
// switch is flipped, or a write overtaken by the next flip, does not put the old value back.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./api";
import { resetManagerChoice, settleRosterDefault } from "./manager-choice";
import { getWorkspaceBoardSetting, refreshWorkspaceBoardSetting, resetWorkspaceBoardSettingForTest, setWorkspaceBoardEnabled } from "./workspace-board-setting";

// the hub's pushes, by type, as the switch's module listens for them
const pushes = vi.hoisted(() => new Map<string, (msg: Record<string, unknown>) => void>());
vi.mock("./store", async (orig) => ({
  ...(await orig<typeof import("./store")>()),
  onHubEvent: (type: string, l: (msg: Record<string, unknown>) => void) => {
    pushes.set(type, l);
    return () => {};
  },
}));

interface Held {
  method: string;
  answer: (body: unknown, status?: number) => void;
}

/** Every /workspace-board/settings request waits until the test answers it, in whatever order it likes. */
function heldHub(): Held[] {
  const held: Held[] = [];
  vi.stubGlobal("fetch", vi.fn().mockImplementation((url: string, init?: RequestInit) => {
    if (!url.includes("/workspace-board/settings")) return Promise.resolve(new Response("{}", { status: 200 }));
    return new Promise<Response>((r) => held.push({ method: init?.method ?? "GET", answer: (body, status = 200) => r(new Response(JSON.stringify(body), { status })) }));
  }));
  return held;
}

const settle = () => new Promise((r) => setTimeout(r, 0));

beforeEach(() => {
  resetWorkspaceBoardSettingForTest();
  vi.stubGlobal("localStorage", { getItem: () => "mock-token", setItem: () => {}, removeItem: () => {} });
});

afterEach(() => {
  vi.unstubAllGlobals();
  resetWorkspaceBoardSettingForTest();
});

describe("workspace board switch: an older answer does not put back an old value", () => {
  it("a read on its way when the switch is turned on does not turn it back off", async () => {
    const held = heldHub();
    const read = refreshWorkspaceBoardSetting();
    await settle();
    const write = setWorkspaceBoardEnabled(true);
    await settle();
    expect(held.map((h) => h.method)).toEqual(["GET", "PUT"]);
    held[1].answer({ enabled: true, can_write: true });
    await write;
    held[0].answer({ enabled: false, can_write: true }); // what the hub had before the write
    await read;
    expect(getWorkspaceBoardSetting()).toEqual({ enabled: true, canWrite: true });
  });

  it("a write overtaken by the next flip does not undo it", async () => {
    const held = heldHub();
    const on = setWorkspaceBoardEnabled(true);
    const off = setWorkspaceBoardEnabled(false);
    await settle();
    held[1].answer({ enabled: false, can_write: true });
    await off;
    held[0].answer({ enabled: true, can_write: true });
    await on;
    expect(getWorkspaceBoardSetting().enabled).toBe(false);
  });

  it("a failed write overtaken by the next flip does not put back what was before it", async () => {
    const held = heldHub();
    const on = setWorkspaceBoardEnabled(true).catch(() => "failed");
    const off = setWorkspaceBoardEnabled(false);
    await settle();
    held[1].answer({ enabled: false, can_write: true });
    await off;
    held[0].answer({ error: "busy" }, 503);
    expect(await on).toBe("failed");
    expect(getWorkspaceBoardSetting().enabled).toBe(false);
  });

  it("answers that come back in order are all taken", async () => {
    const held = heldHub();
    const read = refreshWorkspaceBoardSetting();
    await settle();
    held[0].answer({ enabled: false, can_write: true });
    await read;
    expect(getWorkspaceBoardSetting()).toEqual({ enabled: false, canWrite: true });
    const write = setWorkspaceBoardEnabled(true);
    await settle();
    held[1].answer({ enabled: true, can_write: true });
    await write;
    expect(getWorkspaceBoardSetting()).toEqual({ enabled: true, canWrite: true });
  });
});

describe("workspace board switch: a read asked for while one is out", () => {
  it("a hello while a read is out reads the switch again after it, so a change missed meanwhile is not lost", async () => {
    const held = heldHub();
    const first = refreshWorkspaceBoardSetting();
    await settle();
    const again = refreshWorkspaceBoardSetting(); // the hub came back (hello) after another device turned the board on
    held[0].answer({ enabled: false, can_write: true });
    await settle();
    expect(held.map((h) => h.method)).toEqual(["GET", "GET"]);
    held[1].answer({ enabled: true, can_write: true });
    await Promise.all([first, again]); // both wait for the read after: callers see the newest value
    expect(getWorkspaceBoardSetting()).toEqual({ enabled: true, canWrite: true });
  });

  it("hellos while a read is out add one read after it, not one each", async () => {
    const held = heldHub();
    const first = refreshWorkspaceBoardSetting();
    await settle();
    void refreshWorkspaceBoardSetting();
    void refreshWorkspaceBoardSetting();
    held[0].answer({ enabled: true, can_write: true });
    await settle();
    expect(held).toHaveLength(2);
    held[1].answer({ enabled: false, can_write: true });
    await first;
    await settle();
    expect(held).toHaveLength(2);
    expect(getWorkspaceBoardSetting().enabled).toBe(false);
  });
});

describe("workspace board switch: a read that fails", () => {
  it("with nothing known yet, keeps why (not loading forever), and the next read that works clears it", async () => {
    const held = heldHub();
    const read = refreshWorkspaceBoardSetting();
    await settle();
    held[0].answer({ error: "database is down", code: "internal" }, 500);
    await read;
    expect(getWorkspaceBoardSetting().enabled).toBeNull();
    expect((getWorkspaceBoardSetting().error as ApiError).status).toBe(500);

    const again = refreshWorkspaceBoardSetting(); // Retry, or the next hello
    await settle();
    held[1].answer({ enabled: true, can_write: true });
    await again;
    expect(getWorkspaceBoardSetting()).toEqual({ enabled: true, canWrite: true });
  });

  it("after the hub answered once, keeps what it said and adds no error", async () => {
    const held = heldHub();
    const first = refreshWorkspaceBoardSetting();
    await settle();
    held[0].answer({ enabled: true, can_write: true });
    await first;
    const second = refreshWorkspaceBoardSetting();
    await settle();
    held[1].answer({ error: "busy" }, 503);
    await second;
    expect(getWorkspaceBoardSetting()).toEqual({ enabled: true, canWrite: true });
  });

  it("404 (a hub from before the switch): the board shows, and the switch is missing rather than read-only", async () => {
    const held = heldHub();
    const read = refreshWorkspaceBoardSetting();
    await settle();
    held[0].answer({ error: "Not Found", code: "not_found" }, 404);
    await read;
    expect(getWorkspaceBoardSetting()).toMatchObject({ enabled: true, canWrite: false });
    expect((getWorkspaceBoardSetting().error as ApiError).status).toBe(404);
  });
});

describe("workspace board switch: a push after a read that failed", () => {
  const settled = async () => {
    for (let i = 0; i < 5; i++) await settle();
  };
  afterEach(() => resetManagerChoice());

  it("takes on or off from the push, but not that this device can't change it, and reads the switch again for that", async () => {
    const held = heldHub();
    settleRosterDefault(); // the roster names the default manager, whose switch the page reads
    await settled();
    expect(held).toHaveLength(1);
    held[0].answer({ error: "database is down", code: "internal" }, 500);
    await settled();
    expect(getWorkspaceBoardSetting().enabled).toBeNull();

    pushes.get("workspace_board_settings")!({ type: "workspace_board_settings", enabled: true }); // another device turned it on
    expect(getWorkspaceBoardSetting().enabled).toBe(true);
    // who may change it is still unknown: the failure stays, so Settings doesn't call this device read-only
    expect((getWorkspaceBoardSetting().error as ApiError).status).toBe(500);
    await settled();
    expect(held.map((h) => h.method)).toEqual(["GET", "GET"]);
    held[1].answer({ enabled: true, can_write: true });
    await settled();
    expect(getWorkspaceBoardSetting()).toEqual({ enabled: true, canWrite: true });
  });

  it("after a read that worked, a push changes only on or off and reads nothing more", async () => {
    const held = heldHub();
    settleRosterDefault();
    await settled();
    held[0].answer({ enabled: false, can_write: false });
    await settled();
    pushes.get("workspace_board_settings")!({ type: "workspace_board_settings", enabled: true });
    await settled();
    expect(held).toHaveLength(1);
    expect(getWorkspaceBoardSetting()).toEqual({ enabled: true, canWrite: false });
  });
});
