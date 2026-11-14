import { describe, expect, it } from "vitest";
import { pushedNotification, pushTag, pushUrl, readPushMessage } from "./sw-push";

const scope = "https://hub.example/";

describe("pushedNotification (#7459)", () => {
  it("shows a card as the phone does: the title, then the conversation and computer over what it asks", () => {
    const n = pushedNotification(
      { v: 1, kind: "interaction", title: "Needs your permission", subtitle: "Fix the login · Laptop", body: "Bash: npm test", machine_id: "laptop", session_id: "s 1", interaction_id: "ix1" },
      scope,
    );
    expect(n.title).toBe("Needs your permission");
    expect(n.options.body).toBe("Fix the login · Laptop\nBash: npm test");
    expect(n.options.tag).toBe("laptop/s 1#needs"); // the page's tag for the same agent
    expect(n.options.silent).toBe(false);
    expect(n.options.data).toEqual({ url: "https://hub.example/s/laptop/s%201", pushed: true, kind: "interaction" });
    expect(n.options.icon).toBe("https://hub.example/app-icon-192.png");
  });

  it("tags each kind as the page does, and opens what it is about", () => {
    const cases: [Record<string, unknown>, string | undefined, string][] = [
      [{ kind: "done", machine_id: "m", session_id: "s" }, "m/s#finished", "https://hub.example/s/m/s"],
      [{ kind: "reply", machine_id: "m", session_id: "s" }, "m/s#needs", "https://hub.example/s/m/s"],
      [{ kind: "interaction", interaction_id: "ix/2" }, "card:ix/2#needs", "https://hub.example/requests/ix%2F2"],
      [{ kind: "assistant", todo_id: "td1" }, "todo:td1", "https://hub.example/todos/td1"],
      [{ kind: "alert", id: "a1" }, "alert:a1", "https://hub.example/"],
      [{ kind: "alert" }, undefined, "https://hub.example/"],
      [{ kind: "test" }, "webpush-test", "https://hub.example/"],
    ];
    for (const [msg, tag, url] of cases) {
      expect(pushTag(readPushMessage(msg))).toBe(tag);
      expect(pushUrl(readPushMessage(msg), scope)).toBe(url);
    }
    expect(pushUrl(readPushMessage({ machine_id: "m", session_id: "s" }), "https://hub.example")).toBe("https://hub.example/s/m/s");
  });

  it("comes without a sound inside quiet hours", () => {
    expect(pushedNotification({ kind: "interaction", title: "t", quiet: true }, scope).options.silent).toBe(true);
  });

  it("is still a notification for a push it can't read: the browser revokes a subscription whose pushes show nothing", () => {
    for (const raw of [undefined, null, "text", 3, [], { kind: 5, title: {} }]) {
      const n = pushedNotification(raw, scope);
      expect(n.title).toBe("Persona");
      expect(n.options.data.url).toBe(scope);
    }
    // no title: the subtitle is the title
    expect(pushedNotification({ subtitle: "Laptop", body: "Done" }, scope)).toMatchObject({ title: "Laptop", options: { body: "Done" } });
  });

  it("keeps a long body to what a notification shows", () => {
    const n = pushedNotification({ title: "t", body: "x".repeat(2000) }, scope);
    expect(n.options.body.length).toBe(400);
    expect(n.options.body.endsWith("…")).toBe(true);
  });
});
