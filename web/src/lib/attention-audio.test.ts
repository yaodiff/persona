// @vitest-environment jsdom
// The alert sound's AudioContext (web usability audit item 89): making one takes 80–130 ms of the gesture it runs in, so
// with the sound off (the default) the page's first key or click must not make it; on, the first gesture does, and
// switching it on by a click (a gesture itself) does at once.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

let made = 0;
let resumed = 0;
class FakeAudioContext {
  state = "suspended";
  constructor() {
    made++;
  }
  resume() {
    resumed++;
    this.state = "running";
    return Promise.resolve();
  }
}

const press = () => window.dispatchEvent(new KeyboardEvent("keydown", { key: "k", bubbles: true }));
const tap = () => window.dispatchEvent(new Event("pointerdown"));

let armAudio: typeof import("./attention-signals").armAudio;
let setSound: typeof import("./attention").setAttentionSoundEnabled;
let stop: () => void = () => {};

beforeEach(async () => {
  made = resumed = 0;
  localStorage.clear();
  vi.stubGlobal("AudioContext", FakeAudioContext);
  vi.resetModules(); // a fresh module: its AudioContext is not made yet
  ({ armAudio } = await import("./attention-signals"));
  ({ setAttentionSoundEnabled: setSound } = await import("./attention"));
});

afterEach(() => {
  stop();
  vi.unstubAllGlobals();
});

describe("armAudio", () => {
  it("makes no AudioContext on the first key or click while the alert sound is off", () => {
    stop = armAudio();
    press();
    tap();
    expect(made).toBe(0);
  });

  it("makes it on the first gesture once the sound is on, and only once", () => {
    localStorage.setItem("persona.attentionSound", "1");
    stop = armAudio();
    expect(made).toBe(0); // no gesture yet: browsers would refuse to start it
    press();
    expect([made, resumed]).toEqual([1, 1]);
    tap();
    press();
    expect(made).toBe(1);
  });

  it("follows the switch: off again before a gesture makes nothing, on later arms the next gesture", () => {
    stop = armAudio();
    setSound(true);
    setSound(false);
    press();
    expect(made).toBe(0);
    setSound(true);
    tap();
    expect(made).toBe(1);
  });

  it("makes it at once when the switch is turned on by a click, which is itself the gesture", () => {
    vi.stubGlobal("navigator", { ...navigator, userActivation: { isActive: true, hasBeenActive: true } });
    stop = armAudio();
    expect(made).toBe(0);
    setSound(true);
    expect([made, resumed]).toEqual([1, 1]);
  });

  it("stops listening once cleaned up", () => {
    stop = armAudio();
    stop();
    setSound(true);
    press();
    expect(made).toBe(0);
  });
});
