// A language picked in another tab of the browser: this page reloads into it, but never over what would be lost. The
// first version reloaded at once, whatever the page held: half a new to-do or an Inbox reply typed in a dialog went.
import { describe, expect, it } from "vitest";
import { languageFollower } from "./language-sync";
import { QUIET_MS, type Calm } from "./new-version";

function setup(o: { calm?: Partial<Calm>; differs?: boolean } = {}) {
  const log: string[] = [];
  let calm: Calm = { hidden: true, draft: false, overlay: false, quietMs: 0, ...o.calm };
  let differs = o.differs ?? true;
  let reloadLater: (() => void) | undefined;
  const f = languageFollower({
    differs: () => differs,
    calm: () => calm,
    reload: () => log.push("reload"),
    notify: (reloadNow) => {
      log.push("notify");
      reloadLater = reloadNow;
    },
  });
  return {
    f,
    log,
    setCalm: (x: Partial<Calm>) => (calm = { ...calm, ...x }),
    setDiffers: (x: boolean) => (differs = x),
    click: () => reloadLater?.(),
  };
}

describe("a language another tab picked", () => {
  it("reloads a page in the background with nothing to lose at once", () => {
    const t = setup();
    t.f.changed();
    expect(t.log).toEqual(["reload"]);
    t.f.tick();
    expect(t.log).toEqual(["reload"]);
  });

  it("waits over a draft, says so once, and reloads when the draft is gone", () => {
    const t = setup({ calm: { draft: true } });
    t.f.changed();
    t.f.tick();
    expect(t.log).toEqual(["notify"]);
    t.setCalm({ draft: false });
    t.f.tick();
    expect(t.log).toEqual(["notify", "reload"]);
  });

  it("waits over an open dialog or menu, even in the background", () => {
    const t = setup({ calm: { overlay: true } });
    t.f.changed();
    t.f.tick();
    expect(t.log).toEqual(["notify"]);
    t.setCalm({ overlay: false });
    t.f.tick();
    expect(t.log).toEqual(["notify", "reload"]);
  });

  it("waits while the page in front is in use, until it is left alone", () => {
    const t = setup({ calm: { hidden: false, quietMs: 5_000 } });
    t.f.changed();
    expect(t.log).toEqual(["notify"]);
    t.setCalm({ quietMs: QUIET_MS });
    t.f.tick();
    expect(t.log).toEqual(["notify", "reload"]);
  });

  it("reloads from the toast's button whenever the user wants, once", () => {
    const t = setup({ calm: { draft: true } });
    t.f.changed();
    t.click();
    t.click();
    t.f.tick();
    expect(t.log).toEqual(["notify", "reload"]);
  });

  it("stays when the language is this page's already, or was set back before the page was calm", () => {
    const same = setup({ differs: false });
    same.f.changed();
    expect(same.log).toEqual([]);
    const back = setup({ calm: { draft: true } });
    back.f.changed();
    back.setDiffers(false);
    back.setCalm({ draft: false });
    back.f.tick();
    expect(back.log).toEqual(["notify"]);
  });

  it("does nothing until another tab saved a choice (a system language that changed is not one)", () => {
    const t = setup();
    t.f.tick();
    expect(t.log).toEqual([]);
  });
});
