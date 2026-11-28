import { expect, it } from "vitest";
import type { MenuItem } from "../ui/menu-model";
import { groupAgentMenu } from "./agent-menu-layout";

const action = (key: string): MenuItem => ({ key, label: key, onSelect: () => {} });
const keys = (items: MenuItem[]) => items.map((item) => item.key);

it("keeps the iOS order for every subset of primary actions and either destructive action", () => {
  const primary = ["back-to-chat", "stop", "seen", "rename", "pin"];
  const secondary = ["files", "mute", "cwd"];
  for (let mask = 0; mask < 1 << primary.length; mask++) {
    const offered = primary.filter((_, index) => mask & (1 << index));
    for (const last of [[], ["delete"], ["close"], ["close", "delete"]]) {
      const items = groupAgentMenu([...last, ...offered, ...secondary].reverse().map(action), [action("git")]);
      expect(keys(items)).toEqual([...offered, "more", ...last.slice(0, 1)]);
      expect(items.length).toBeLessThanOrEqual(7);
      const children = items.find((it) => it.key === "more")!.children as MenuItem[];
      expect(keys(children)).toEqual([...secondary, "pane:git", ...last.slice(1)]);
    }
  }
});

it("preserves an action's submenu, confirmation and callback, and omits an empty More", () => {
  const git = { ...action("node-git"), children: [{ ...action("push"), confirm: "Push?" }] };
  const items = groupAgentMenu([], [git]);
  expect((items[0].children as MenuItem[])[0]).toMatchObject({ ...git, key: "pane:node-git" });
  expect(keys(groupAgentMenu([action("delete")]))).toEqual(["delete"]);
});

it("keeps a panel's own Delete separate from deleting the Agent chat", () => {
  const removeTask = action("delete");
  const items = groupAgentMenu([action("close"), action("delete")], [removeTask]);
  const children = items[0].children as MenuItem[];
  expect(keys(children)).toEqual(["pane:delete", "delete"]);
  expect(children[0].onSelect).toBe(removeTask.onSelect);
});
