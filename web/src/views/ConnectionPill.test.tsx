// @vitest-environment jsdom
// The pill over the page once the socket dropped (#2970): its words follow the phase, a click opens the details (why,
// the next try, since when), "Retry now" tries now, and Escape or a click elsewhere closes them.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { NO_LINK, type ConnLink } from "../lib/connection";
import type { Conn } from "../lib/store";

const fake = vi.hoisted(() => ({ conn: "offline" as string, link: null as unknown, reconnect: () => {} }));
vi.mock("../lib/store", () => ({
  useStore: <T,>(sel: (s: { conn: string; link: unknown }) => T) => sel({ conn: fake.conn, link: fake.link }),
  reconnectNow: () => fake.reconnect(),
}));

import { ConnectionDetails, ConnectionPill } from "./ConnectionPill";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const now = 1_000_000;
let host: HTMLDivElement;
let root: Root;
const pill = () => host.querySelector<HTMLButtonElement>("[data-connection-pill]")!;
const details = () => host.querySelector("[data-connection-details]");

beforeEach(() => {
  fake.conn = "offline";
  fake.link = { ...NO_LINK, nextTry: Date.now() + 4_200, lastOnline: Date.now() - 75_000 } satisfies ConnLink;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("ConnectionPill", () => {
  it("says the phase; a click opens the details with the facts, a second click closes them", () => {
    act(() => root.render(<ConnectionPill />));
    expect(pill().textContent).toBe("无法连接 hub，正在重试…");
    expect(pill().getAttribute("aria-expanded")).toBe("false");
    expect(details()).toBeNull();
    act(() => pill().click());
    expect(pill().getAttribute("aria-expanded")).toBe("true");
    expect(details()?.textContent).toContain("秒后重试");
    expect(details()?.textContent).toContain("上次连接：1 分钟前");
    act(() => pill().click());
    expect(details()).toBeNull();
  });

  it("Escape and a pointerdown outside close the details; one inside does not", () => {
    act(() => root.render(<ConnectionPill />));
    act(() => pill().click());
    act(() => details()!.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true })));
    expect(details()).not.toBeNull();
    act(() => document.body.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true })));
    expect(details()).toBeNull();
    act(() => pill().click());
    act(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    expect(details()).toBeNull();
  });

  it("a restarting hub has its own words and a breathing dot", () => {
    fake.conn = "offline";
    fake.link = { ...NO_LINK, restarting: true };
    act(() => root.render(<ConnectionPill />));
    expect(pill().textContent).toBe("hub 正在更新…");
    const dot = pill().querySelector("span[aria-hidden]") as HTMLElement;
    expect(dot.className).toContain("bg-orange");
    expect(dot.style.animation).toContain("breathe");
  });
});

describe("ConnectionDetails", () => {
  const render = (conn: Conn, link: ConnLink) => act(() => root.render(<ConnectionDetails conn={conn} link={link} now={now} />));

  it("立即重试 tries now and shows that it is trying", () => {
    const reconnect = vi.fn();
    fake.reconnect = reconnect;
    render("offline", { ...NO_LINK, nextTry: now + 4_200, lastOnline: now - 12_000 });
    expect(details()?.textContent).toContain("5 秒后重试");
    expect(details()?.textContent).toContain("上次连接：12 秒前");
    const button = details()!.querySelector("button")!;
    expect(button.textContent).toContain("立即重试");
    act(() => button.click());
    expect(reconnect).toHaveBeenCalledTimes(1);
    expect(button.disabled).toBe(true); // busy: a spinner while it tries
  });

  it("online: no facts and no 立即重试", () => {
    render("online", { ...NO_LINK, nextTry: now + 1_000 });
    expect(details()?.querySelector("button")).toBeNull();
    expect(details()?.querySelector("ul")).toBeNull();
  });
});
