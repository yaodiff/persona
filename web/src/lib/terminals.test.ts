import { describe, expect, it } from "vitest";
import {
  homeShellHost,
  buildTerminalTree,
  filterTerminalTree,
  isPaneAddress,
  keepOnlyShown,
  keepRecent,
  paneCwd,
  paneIds,
  paneRow,
  paneState,
  paneTrail,
  terminalId,
  type PaneDTO,
  type PanesResponse,
} from "./terminals";
import { href, parse } from "./router";
import type { Session } from "./types";

const res: PanesResponse = {
  hosts: [
    {
      kind: "herdr",
      stream: true,
      control: true,
      new_tab: true,
      workspaces: [
        { id: "w2", label: "docs", number: 2 },
        { id: "w1", label: "proj", number: 1, cwd: "/home/me/proj", branch: "main" },
      ],
      tabs: [
        { id: "w1:t2", workspace_id: "w1", label: "shell", number: 2 },
        { id: "w1:t1", workspace_id: "w1", label: "agent-1", number: 1 },
      ],
      panes: [
        { pane: "w1:p2", terminal: "pane:w1:p2", tab_id: "w1:t2", workspace_id: "w1", title: "zsh", cwd: "/home/me/proj" },
        { pane: "w1:p10", terminal: "pane:w1:p10", tab_id: "w1:t1", workspace_id: "w1", agent: "claude", status: "blocked", cwd: "/home/me/proj" },
        { pane: "w1:p1", terminal: "pane:w1:p1", tab_id: "w1:t1", workspace_id: "w1", agent: "claude", status: "working", title: "fix it", cwd: "/home/me/proj", session_id: "sid-1" },
      ],
    },
  ],
};

const session = (over: Partial<Session> = {}) => ({ machine_id: "m1", session_id: "sid-1", title: "修登录崩溃", cwd: "/home/me/proj", status: "idle", ...over }) as Session;

describe("the terminal browser's tree", () => {
  it("lays workspaces, tabs and panes out in herdr's order, shells and agents alike", () => {
    const tree = buildTerminalTree(res, [session({ state: "unread" })]);
    // w2 has no tab with a pane, but a host that opens tabs still offers it ("New terminal")
    expect(tree.map((s) => s.id)).toEqual(["w1", "w2"]);
    const w1 = tree[0];
    expect(w1).toMatchObject({ label: "proj", branch: "main", host: "herdr", canOpen: true });
    expect(w1.tabs.map((t) => t.label)).toEqual(["agent-1", "shell"]);
    expect(w1.tabs[0].panes.map((p) => p.id)).toEqual(["sid-1", "pane:w1:p10"]); // p1 before p10, numerically
    expect(tree[1].tabs).toEqual([]);
  });

  it("badges an agent by its session's state, else the host's, and a shell by none", () => {
    const [w1] = buildTerminalTree(res, [session({ state: "unread" })]);
    const [agentTab, shellTab] = w1.tabs;
    expect(agentTab.panes[0].state).toBe("unread"); // the session knows it is unread; the host said working
    expect(agentTab.panes[1].state).toBe("needs_you"); // no session: blocked is needs you
    expect(shellTab.panes[0]).toMatchObject({ state: "shell", title: "zsh", agent: "" });
  });

  it("opens an agent by its session (it shares that stream) and anything else by the pane's address", () => {
    expect(terminalId(res.hosts[0].panes[2])).toBe("sid-1");
    expect(terminalId(res.hosts[0].panes[0])).toBe("pane:w1:p2");
    expect(isPaneAddress("pane:w1:p2")).toBe(true);
    expect(isPaneAddress("11111111-2222")).toBe(false);
  });

  it("names panes by what they are", () => {
    const shell: PaneDTO = { pane: "w1:p3", terminal: "pane:w1:p3", tab_id: "t", workspace_id: "w", cwd: "/home/me/proj" };
    expect(paneRow(shell, undefined)).toEqual({ title: "proj", detail: "~/proj" });
    expect(paneRow({ ...shell, title: "zsh" }, undefined)).toEqual({ title: "zsh", detail: "~/proj" });
    expect(paneRow({ ...shell, cwd: undefined }, undefined).title).toBe("Shell");
    expect(paneRow({ ...shell, agent: "codex" }, undefined)).toEqual({ title: "codex", detail: "codex · ~/proj" });
    expect(paneRow({ ...shell, agent: "claude" }, session())).toEqual({ title: "修登录崩溃", detail: "claude · ~/proj" });
    expect(paneState({ ...shell, agent: "claude", status: "mystery" }, undefined)).toBe("unknown");
  });

  it("keeps panes whose workspace or tab the host did not list", () => {
    const odd: PanesResponse = {
      hosts: [{ kind: "herdr", stream: true, control: false, new_tab: false, workspaces: [], tabs: [], panes: [{ pane: "w9:p1", terminal: "pane:w9:p1", tab_id: "w9:t1", workspace_id: "w9", title: "zsh" }] }],
    };
    const tree = buildTerminalTree(odd, []);
    expect(tree.map((s) => [s.id, s.tabs.map((t) => t.id), s.canOpen])).toEqual([["w9", ["w9:t1"], false]]);
  });

  // what a computer with both answers: botmux has no tabs, and every pane of it has an empty tab id
  const herdrAndBotmux: PanesResponse = {
    hosts: [
      {
        kind: "herdr",
        stream: true,
        control: true,
        new_tab: true,
        workspaces: [
          { id: "w2", label: "myherdr", number: 1 },
          { id: "w4", label: "mybrowser", number: 2 },
        ],
        tabs: [
          { id: "w2:t1", workspace_id: "w2", label: "shell", number: 1 },
          { id: "w4:t1", workspace_id: "w4", label: "shell", number: 1 },
        ],
        panes: [
          { pane: "w2:p1", terminal: "pane:w2:p1", tab_id: "w2:t1", workspace_id: "w2", title: "zsh" },
          { pane: "w4:p1", terminal: "pane:w4:p1", tab_id: "w4:t1", workspace_id: "w4", title: "zsh" },
        ],
      },
      {
        kind: "botmux",
        stream: false,
        control: false,
        new_tab: false,
        workspaces: [
          { id: "dir:/home/me/a/research", label: "research", number: 1, cwd: "/home/me/a/research" },
          { id: "dir:/home/me/b/notes", label: "notes", number: 2, cwd: "/home/me/b/notes" },
          { id: "dir:/home/me/b/trade", label: "trade", number: 3, cwd: "/home/me/b/trade" },
        ],
        tabs: [],
        panes: [
          { pane: "botmux:id1", terminal: "pane:botmux:id1", tab_id: "", workspace_id: "dir:/home/me/a/research", agent: "claude", status: "working", cwd: "/home/me/a/research", session_id: "id1" },
          { pane: "botmux:id2", terminal: "pane:botmux:id2", tab_id: "", workspace_id: "dir:/home/me/b/notes", agent: "claude", status: "idle", cwd: "/home/me/b/notes", session_id: "id2" },
          { pane: "botmux:id3", terminal: "pane:botmux:id3", tab_id: "", workspace_id: "dir:/home/me/b/trade", agent: "codex", status: "idle", cwd: "/home/me/b/trade" },
        ],
      },
    ],
  };

  it("lists under each botmux workspace only its own sessions, with no tabs and empty tab ids", () => {
    const tree = buildTerminalTree(herdrAndBotmux, []);
    const rows = tree.filter((s) => s.host === "botmux").map((s) => [s.id, s.tabs.map((t) => t.panes.map((p) => p.pane))]);
    expect(rows).toEqual([
      ["dir:/home/me/a/research", [["botmux:id1"]]],
      ["dir:/home/me/b/notes", [["botmux:id2"]]],
      ["dir:/home/me/b/trade", [["botmux:id3"]]],
    ]);
    expect(paneIds(tree)).toEqual(["pane:w2:p1", "pane:w4:p1", "id1", "id2", "pane:botmux:id3"]); // one row a pane
  });

  it("leaves herdr's workspaces as they were beside botmux's", () => {
    const shape = (spaces: ReturnType<typeof buildTerminalTree>) => spaces.map((s) => [s.id, s.canOpen, s.tabs.map((t) => [t.id, t.label, t.panes.map((p) => p.id)])]);
    const expected = [
      ["w2", true, [["w2:t1", "shell", ["pane:w2:p1"]]]],
      ["w4", true, [["w4:t1", "shell", ["pane:w4:p1"]]]],
    ];
    expect(shape(buildTerminalTree({ hosts: [herdrAndBotmux.hosts[0]] }, []))).toEqual(expected);
    expect(shape(buildTerminalTree(herdrAndBotmux, []).filter((s) => s.host === "herdr"))).toEqual(expected);
  });

  it("is empty for a machine with nothing yet", () => {
    expect(buildTerminalTree(undefined, [])).toEqual([]);
    expect(buildTerminalTree({ hosts: [] }, [])).toEqual([]);
  });
});

describe("Jump To", () => {
  const tree = buildTerminalTree(res, [session()]);

  it("matches every word against the row and what is around it", () => {
    expect(paneIds(filterTerminalTree(tree, ""))).toEqual(["sid-1", "pane:w1:p10", "pane:w1:p2"]);
    expect(paneIds(filterTerminalTree(tree, "zsh"))).toEqual(["pane:w1:p2"]);
    expect(paneIds(filterTerminalTree(tree, "proj shell"))).toEqual(["pane:w1:p2"]); // workspace + tab
    expect(paneIds(filterTerminalTree(tree, "登录"))).toEqual(["sid-1"]);
    expect(paneIds(filterTerminalTree(tree, "main claude"))).toEqual(["sid-1", "pane:w1:p10"]); // branch + agent
    expect(paneIds(filterTerminalTree(tree, "nothing like it"))).toEqual([]);
  });

  it("matches the machine's name too, and drops what has no match", () => {
    expect(paneIds(filterTerminalTree(tree, "studio zsh", "Studio"))).toEqual(["pane:w1:p2"]);
    expect(filterTerminalTree(tree, "zsh").map((s) => s.id)).toEqual(["w1"]);
  });

  it("writes where a pane is", () => {
    expect(paneTrail(tree, "pane:w1:p2")).toBe("proj › shell › zsh");
    expect(paneTrail(tree, "sid-1")).toBe("proj › agent-1 › 修登录崩溃");
    expect(paneTrail(tree, "gone")).toBe("");
    expect(paneCwd(tree, "pane:w1:p2")).toBe("/home/me/proj");
    expect(paneCwd(tree, "gone")).toBe("");
  });
});

describe("terminals kept open", () => {
  const t = (mid: string, id: string) => ({ mid, id });

  it("keeps the last two of a machine, newest first", () => {
    let list = keepRecent([], t("a", "1"));
    list = keepRecent(list, t("a", "2"));
    expect(list).toEqual([t("a", "2"), t("a", "1")]);
    list = keepRecent(list, t("a", "3"));
    expect(list).toEqual([t("a", "3"), t("a", "2")]); // "1" is closed
  });

  it("counts each machine on its own", () => {
    let list = keepRecent([], t("a", "1"));
    list = keepRecent(list, t("b", "1"));
    list = keepRecent(list, t("a", "2"));
    list = keepRecent(list, t("b", "2"));
    expect(list).toEqual([t("b", "2"), t("a", "2"), t("b", "1"), t("a", "1")]);
    list = keepRecent(list, t("a", "3"));
    expect(list).toEqual([t("a", "3"), t("b", "2"), t("a", "2"), t("b", "1")]);
  });

  it("going back to one kept moves it up and closes nothing", () => {
    const list = [t("a", "2"), t("a", "1")];
    expect(keepRecent(list, t("a", "1"))).toEqual([t("a", "1"), t("a", "2")]);
  });

  it("keeps only the shown one when the page goes to the background", () => {
    const list = [t("a", "2"), t("a", "1"), t("b", "1")];
    expect(keepOnlyShown(list, t("a", "2"))).toEqual([t("a", "2")]);
    expect(keepOnlyShown(list, undefined)).toEqual([]);
  });
});

describe("the page's address", () => {
  it("round-trips a terminal, ids with colons included", () => {
    const to = { name: "terminal" as const, mid: "mac", id: "pane:w1:p2" };
    expect(href(to)).toBe("/terminal/mac/pane%3Aw1%3Ap2");
    expect(parse(href(to))).toEqual(to);
    expect(parse("/terminal")).toEqual({ name: "terminal" });
    expect(href({ name: "terminal" })).toBe("/terminal");
    expect(parse("/terminal/mac")).toEqual({ name: "terminal" }); // half an address is the list
  });
});

describe("New terminal with no workspace", () => {
  const host = (kind: string, newTab: boolean) => ({ kind, stream: true, control: true, new_tab: newTab, workspaces: [], tabs: [], panes: [] });
  it("opens in the first engine that opens tabs, none when no engine can", () => {
    expect(homeShellHost({ hosts: [host("orca", false), host("herdr", true)] })).toBe("herdr");
    expect(homeShellHost({ hosts: [host("orca", false)] })).toBeUndefined();
    expect(homeShellHost(undefined)).toBeUndefined();
  });
});
