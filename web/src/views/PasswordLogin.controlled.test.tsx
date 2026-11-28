// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { NativeLogin, PasswordCredential, PasswordState } from "../lib/password";

const globals = vi.hoisted(() => ({
  passwordApi: vi.fn<(path: string, json?: unknown) => Promise<PasswordState>>(),
  loginWithPassword: vi.fn<(password: string, native: NativeLogin | null, options?: { replaces?: boolean }) => Promise<PasswordCredential | { code: string }>>(),
  readNativeLogin: vi.fn<(href: string) => NativeLogin | null>(),
  api: vi.fn<(path: string, init?: { method?: string; json?: unknown; token?: string; signal?: AbortSignal }) => Promise<unknown>>(),
  setPairedToken: vi.fn<(token: string, id?: string) => void>(),
  setToken: vi.fn<(token: string, id?: string) => void>(),
  errorText: vi.fn<(err: unknown) => string>((e) => (e instanceof Error ? e.message : String(e))),
  pairToken: vi.fn<(input: string) => string>(),
  deviceName: vi.fn<() => string>(),
  unrevokedTokenId: vi.fn<() => string>(),
  forgetUnrevoked: vi.fn<() => void>(),
  storedCredentials: vi.fn<() => { token: string; tokenId: string }>(),
}));

vi.mock("../lib/password", () => ({
  passwordApi: (...args: [path: string, json?: unknown]) => globals.passwordApi(...args),
  loginWithPassword: (password: string, native: NativeLogin | null, options?: { replaces?: boolean }) =>
    globals.loginWithPassword(password, native, options),
  readNativeLogin: (href: string) => globals.readNativeLogin(href),
}));

vi.mock("../lib/auth", async (actual) => {
  const real = await actual<typeof import("../lib/auth")>();
  return {
    ...real,
    pairToken: (input: string) => {
      globals.pairToken(input);
      return real.pairToken(input);
    },
    deviceName: () => {
      globals.deviceName();
      return real.deviceName();
    },
    unrevokedTokenId: () => {
      globals.unrevokedTokenId();
      return real.unrevokedTokenId();
    },
    forgetUnrevoked: () => {
      globals.forgetUnrevoked();
      return real.forgetUnrevoked();
    },
  };
});

vi.mock("../lib/compatibility", async (actual) => {
  const real = await actual<typeof import("../lib/compatibility")>();
  return {
    ...real,
    storedCredentials: (...args: Parameters<typeof real.storedCredentials>) => {
      globals.storedCredentials();
      return real.storedCredentials(...args);
    },
  };
});

vi.mock("../lib/api", async (actual) => {
  const real = await actual<typeof import("../lib/api")>();
  return {
    ...real,
    api: (path: string, init?: { method?: string; json?: unknown; token?: string; signal?: AbortSignal }) => globals.api(path, init),
    setPairedToken: (token: string, id?: string) => {
      globals.setPairedToken(token, id);
      real.setPairedToken(token, id);
    },
    setToken: (token: string, id?: string) => globals.setToken(token, id),
    errorText: (err: unknown) => globals.errorText(err),
  };
});

import { initI18n, t } from "../i18n";
import { ApiError } from "../lib/api";
import { UNREVOKED_KEY } from "../lib/auth";
import {
  NativePasswordLogin,
  PasswordLogin,
  PasswordLoginView,
  type NativePasswordLoginDeps,
  type PasswordLoginDeps,
} from "./PasswordLogin";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const enabledState: PasswordState = { enabled: true, hub_id: "hub-1", hub_name: "Hub", suite: "v1" };
const disabledState: PasswordState = { enabled: false, hub_id: "hub-1", hub_name: "Hub", suite: "v1" };
const nativeA: NativeLogin = { state: "A".repeat(43), challenge: "C".repeat(43), kind: "phone", name: "Phone A" };
const nativeB: NativeLogin = { state: "B".repeat(43), challenge: "D".repeat(43), kind: "desktop", name: "Mac B" };

let host: HTMLDivElement;
let root: Root;

function expectNoGlobalCalls() {
  expect(globals.passwordApi).not.toHaveBeenCalled();
  expect(globals.loginWithPassword).not.toHaveBeenCalled();
  expect(globals.readNativeLogin).not.toHaveBeenCalled();
  expect(globals.api).not.toHaveBeenCalled();
  expect(globals.setPairedToken).not.toHaveBeenCalled();
  expect(globals.setToken).not.toHaveBeenCalled();
  expect(globals.errorText).not.toHaveBeenCalled();
  expect(globals.pairToken).not.toHaveBeenCalled();
  expect(globals.deviceName).not.toHaveBeenCalled();
  expect(globals.unrevokedTokenId).not.toHaveBeenCalled();
  expect(globals.forgetUnrevoked).not.toHaveBeenCalled();
  expect(globals.storedCredentials).not.toHaveBeenCalled();
}

function inputEl(): HTMLInputElement {
  return host.querySelector<HTMLInputElement>('input[type="password"]')!;
}

function submitButton(): HTMLButtonElement {
  return host.querySelector<HTMLButtonElement>('button[type="submit"]')!;
}

function modeToggleButton(): HTMLButtonElement | null {
  return host.querySelector<HTMLButtonElement>('button[type="button"]');
}

function alertText(): string {
  return host.querySelector('[role="alert"]')?.textContent ?? "";
}

async function typeValue(value: string) {
  await act(async () => {
    const el = inputEl();
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function submitForm() {
  await act(async () => {
    host.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  });
}

beforeEach(async () => {
  await initI18n("en");
  localStorage.clear();
  globals.passwordApi.mockReset();
  globals.loginWithPassword.mockReset();
  globals.readNativeLogin.mockReset();
  globals.api.mockReset();
  globals.setPairedToken.mockReset();
  globals.setToken.mockReset();
  globals.errorText.mockReset().mockImplementation((e) => (e instanceof Error ? e.message : String(e)));
  globals.pairToken.mockReset();
  globals.deviceName.mockReset();
  globals.unrevokedTokenId.mockReset();
  globals.forgetUnrevoked.mockReset();
  globals.storedCredentials.mockReset();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});

describe("PasswordLoginView", () => {
  it("renders password mode, token mode, native mode, errors, and appCallback purely with zero singleton calls", async () => {
    const onTokenChange = vi.fn();
    const onSubmit = vi.fn((e: React.FormEvent) => e.preventDefault());
    const onToggleMode = vi.fn();

    await act(async () => {
      root.render(
        <PasswordLoginView
          native={null}
          mode="password"
          passwordEnabled
          loading={false}
          busy={false}
          token="secret"
          error="bad password"
          appCallback=""
          onTokenChange={onTokenChange}
          onSubmit={onSubmit}
          onToggleMode={onToggleMode}
        />,
      );
    });
    expect(inputEl().placeholder).toBe(t("password.connection_password"));
    expect(alertText()).toBe("bad password");
    await act(async () => modeToggleButton()!.click());
    expect(onToggleMode).toHaveBeenCalledOnce();
    await submitForm();
    expect(onSubmit).toHaveBeenCalledOnce();

    await act(async () => {
      root.render(
        <PasswordLoginView
          native={nativeA}
          mode="password"
          passwordEnabled
          loading={false}
          busy={false}
          token=""
          error=""
          appCallback="persona://password-login?code=abc&state=xyz"
          onTokenChange={onTokenChange}
          onSubmit={onSubmit}
          onToggleMode={onToggleMode}
        />,
      );
    });
    expect(host.querySelector("a")?.getAttribute("href")).toBe("persona://password-login?code=abc&state=xyz");
    expect(modeToggleButton()).toBeNull();
    expectNoGlobalCalls();
  });
});

describe("PasswordLogin controlled mode", () => {
  it("supports browser password login, token mode toggle, and error formatting with zero singleton calls", async () => {
    const onDone = vi.fn();
    const deps: PasswordLoginDeps = {
      passwordApi: vi.fn(async () => enabledState),
      loginWithPassword: vi.fn(async () => ({ token: "jd_pw", id: "dev_pw", hub_id: "hub-1" })),
      pair: vi.fn(async () => {}),
      setToken: vi.fn(),
      formatError: vi.fn((err) => (err instanceof Error ? err.message : String(err))),
      assignLocation: vi.fn(),
    };

    await act(async () => {
      root.render(<PasswordLogin onDone={onDone} deps={deps} />);
    });
    expect(deps.passwordApi).toHaveBeenCalledExactlyOnceWith("/state");
    expect(inputEl().disabled).toBe(false);

    // Password login preserves spaces, clears input immediately, persists token, and calls onDone
    await typeValue("  my-pass  ");
    await submitForm();
    expect(deps.loginWithPassword).toHaveBeenCalledExactlyOnceWith("  my-pass  ", null, { replaces: false });
    expect(inputEl().value).toBe("");
    expect(deps.setToken).toHaveBeenCalledExactlyOnceWith("jd_pw", "dev_pw");
    expect(onDone).toHaveBeenCalledOnce();

    // Invalid browser login result (missing token) surfaces password.invalid_result
    vi.mocked(deps.loginWithPassword!).mockResolvedValueOnce({ code: "wrong-shape" });
    await typeValue("bad-shape-pw");
    await submitForm();
    expect(alertText()).toBe(t("password.invalid_result"));
    expect(onDone).toHaveBeenCalledOnce();

    // Toggle to token mode and test invalid-token translation + trimmed pair
    await act(async () => modeToggleButton()!.click());
    expect(inputEl().placeholder).toBe("hub token");
    vi.mocked(deps.pair!).mockRejectedValueOnce(new Error("invalid token"));
    await typeValue("  bad-tok  ");
    await submitForm();
    expect(deps.pair).toHaveBeenCalledWith("bad-tok", expect.any(AbortSignal));
    expect(alertText()).toBe(t("password.invalid_token"));

    vi.mocked(deps.pair!).mockResolvedValueOnce();
    await submitForm();
    expect(onDone).toHaveBeenCalledTimes(2);
    expect(alertText()).toBe("");
    expectNoGlobalCalls();
  });

  it("handles disabled/failing /state, invalid password results, and NativePasswordLogin via controlled deps", async () => {
    const assignLocation = vi.fn();
    const onDone = vi.fn();
    const deps: NativePasswordLoginDeps = {
      readNativeLogin: vi.fn((href) => (href.includes("good") ? nativeA : href.includes("throw") ? (() => { throw new Error("bad"); })() : null)),
      passwordApi: vi.fn(async () => enabledState),
      loginWithPassword: vi.fn(async () => ({ code: "one-time-code" })),
      pair: vi.fn(async () => {}),
      setToken: vi.fn(),
      formatError: vi.fn((err) => (err instanceof Error ? err.message : String(err))),
      assignLocation,
    };

    // Null native request
    await act(async () => {
      root.render(<NativePasswordLogin href="https://hub.example/empty" deps={deps} />);
    });
    expect(alertText()).toBe(t("password.start_from_app"));

    // Invalid native request (throws)
    await act(async () => {
      root.render(<NativePasswordLogin href="https://hub.example/throw" deps={deps} />);
    });
    expect(alertText()).toBe(t("password.invalid_request_full"));

    // Valid native request -> callback URL + assignLocation
    await act(async () => {
      root.render(<NativePasswordLogin href="https://hub.example/good" onDone={onDone} deps={deps} />);
    });
    expect(deps.passwordApi).toHaveBeenCalledExactlyOnceWith("/state");
    await typeValue("native-pw");
    await submitForm();
    const expectedCallback = `persona://password-login?${new URLSearchParams({ code: "one-time-code", state: nativeA.state })}`;
    expect(assignLocation).toHaveBeenCalledExactlyOnceWith(expectedCallback);
    expect(host.querySelector("a")?.getAttribute("href")).toBe(expectedCallback);
    expect(onDone).not.toHaveBeenCalled();

    // Native disabled /state shows password.disabled and keeps input disabled
    await act(async () => {
      root.render(
        <PasswordLogin
          onDone={onDone}
          native={nativeB}
          deps={{
            ...deps,
            passwordApi: vi.fn(async () => disabledState),
          }}
        />,
      );
    });
    expect(alertText()).toBe(t("password.disabled"));
    expect(inputEl().disabled).toBe(true);
    expect(submitButton().disabled).toBe(true);

    // Browser sync throw from /state falls back cleanly to token mode
    await act(async () => {
      root.render(
        <PasswordLogin
          onDone={onDone}
          scopeKey="sync-err"
          deps={{
            ...deps,
            passwordApi: () => {
              throw new Error("sync-state-fail");
            },
          }}
        />,
      );
    });
    expect(inputEl().placeholder).toBe("hub token");
    expect(inputEl().disabled).toBe(false);
    expectNoGlobalCalls();
  });

  it("blocks duplicate synchronous submits and ignores late /state or submit completions after unmount", async () => {
    const statePending = deferred<PasswordState>();
    const loginPending = deferred<PasswordCredential>();
    const onDone = vi.fn();
    const setTok = vi.fn();
    const assignLocation = vi.fn();
    const loginSpy = vi.fn(() => loginPending.promise);

    // 1) Unmount while /state is pending
    await act(async () => {
      root.render(
        <PasswordLogin
          onDone={onDone}
          native={nativeA}
          deps={{
            passwordApi: () => statePending.promise,
            loginWithPassword: loginSpy,
            setToken: setTok,
            assignLocation,
            formatError: (e) => String(e),
          }}
        />,
      );
    });
    await act(async () => root.unmount());
    await act(async () => {
      statePending.resolve(disabledState);
      await Promise.resolve();
    });

    // 2) Sync double-submit + unmount while loginWithPassword is in flight
    root = createRoot(host);
    await act(async () => {
      root.render(
        <PasswordLogin
          onDone={onDone}
          deps={{
            passwordApi: async () => enabledState,
            loginWithPassword: loginSpy,
            setToken: setTok,
            assignLocation,
            formatError: (e) => String(e),
          }}
        />,
      );
    });
    await typeValue("secret-pw");
    await act(async () => {
      const form = host.querySelector("form")!;
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    expect(loginSpy).toHaveBeenCalledOnce();
    expect(submitButton().disabled).toBe(true);

    // Unmount before login resolves -> late resolve must NOT call setToken or onDone
    await act(async () => root.unmount());
    await act(async () => {
      loginPending.resolve({ token: "late-tok", id: "late-id", hub_id: "hub-1" });
      await Promise.resolve();
    });
    expect(setTok).not.toHaveBeenCalled();
    expect(onDone).not.toHaveBeenCalled();
    expect(assignLocation).not.toHaveBeenCalled();
    expectNoGlobalCalls();
  });

  it("fences /state and submit ABA races across scopeKey and native prop changes without clearing newer busy state", async () => {
    const stateQueue: Array<ReturnType<typeof deferred<PasswordState>>> = [];
    const loginQueue: Array<ReturnType<typeof deferred<PasswordCredential | { code: string }>>> = [];
    const onDone = vi.fn();
    const setTok = vi.fn();
    const assignLocation = vi.fn();

    const deps: PasswordLoginDeps = {
      passwordApi: vi.fn(() => {
        const d = deferred<PasswordState>();
        stateQueue.push(d);
        return d.promise;
      }),
      loginWithPassword: vi.fn(() => {
        const d = deferred<PasswordCredential | { code: string }>();
        loginQueue.push(d);
        return d.promise;
      }),
      pair: vi.fn(async () => {}),
      setToken: setTok,
      formatError: (e) => (e instanceof Error ? e.message : String(e)),
      assignLocation,
    };

    // 1) /state ABA: scope A -> B -> A
    await act(async () => {
      root.render(<PasswordLogin onDone={onDone} scopeKey="hub-a" deps={deps} />);
    });
    await act(async () => {
      root.render(<PasswordLogin onDone={onDone} scopeKey="hub-b" deps={deps} />);
    });
    await act(async () => {
      root.render(<PasswordLogin onDone={onDone} scopeKey="hub-a" deps={deps} />);
    });
    expect(stateQueue).toHaveLength(3);

    // Stale A#1 returns disabled and B rejects -> must be ignored while A#2 is still loading
    await act(async () => {
      stateQueue[0]!.resolve(disabledState);
      stateQueue[1]!.reject(new Error("stale-b"));
      await Promise.resolve();
    });
    expect(inputEl().disabled).toBe(true); // still loading A#2

    await act(async () => {
      stateQueue[2]!.resolve(enabledState);
      await Promise.resolve();
    });
    expect(inputEl().disabled).toBe(false);
    expect(inputEl().placeholder).toBe(t("password.connection_password"));

    // 2) Submit ABA: submit on hub-a (#1), switch hub-a -> hub-b -> hub-a, submit #2, then stale #1 settles
    await typeValue("pw-1");
    await submitForm();
    expect(loginQueue).toHaveLength(1);
    expect(submitButton().disabled).toBe(true);

    await act(async () => {
      root.render(<PasswordLogin onDone={onDone} scopeKey="hub-b" deps={deps} />);
    });
    await act(async () => {
      root.render(<PasswordLogin onDone={onDone} scopeKey="hub-a" deps={deps} />);
    });
    await act(async () => {
      stateQueue[stateQueue.length - 1]!.resolve(enabledState);
      await Promise.resolve();
    });

    await typeValue("pw-2");
    await submitForm();
    expect(loginQueue).toHaveLength(2);
    expect(submitButton().disabled).toBe(true);

    // Stale login #1 settles (resolves) while login #2 is in flight -> must NOT call setToken/onDone or clear busy!
    await act(async () => {
      loginQueue[0]!.resolve({ token: "stale-tok", id: "stale-id", hub_id: "hub-a" });
      await Promise.resolve();
    });
    expect(setTok).not.toHaveBeenCalled();
    expect(onDone).not.toHaveBeenCalled();
    expect(submitButton().disabled).toBe(true);

    // Login #2 succeeds
    await act(async () => {
      loginQueue[1]!.resolve({ token: "fresh-tok", id: "fresh-id", hub_id: "hub-a" });
      await Promise.resolve();
    });
    expect(setTok).toHaveBeenCalledExactlyOnceWith("fresh-tok", "fresh-id");
    expect(onDone).toHaveBeenCalledOnce();

    // Submit ABA rejection: stale login #3 rejects while login #4 is in flight -> must NOT show stale error or clear busy!
    await typeValue("pw-3");
    await submitForm();
    const staleRejectLogin = loginQueue[loginQueue.length - 1]!;
    await act(async () => {
      root.render(<PasswordLogin onDone={onDone} scopeKey="hub-c" deps={deps} />);
    });
    await act(async () => {
      root.render(<PasswordLogin onDone={onDone} scopeKey="hub-a" deps={deps} />);
    });
    await act(async () => {
      stateQueue[stateQueue.length - 1]!.resolve(enabledState);
      await Promise.resolve();
    });
    await typeValue("pw-4");
    await submitForm();
    const activeLogin = loginQueue[loginQueue.length - 1]!;
    expect(submitButton().disabled).toBe(true);

    await act(async () => {
      staleRejectLogin.reject(new Error("stale-failure"));
      await Promise.resolve();
    });
    expect(alertText()).toBe("");
    expect(submitButton().disabled).toBe(true);

    await act(async () => {
      activeLogin.resolve({ token: "tok-4", id: "id-4", hub_id: "hub-a" });
      await Promise.resolve();
    });
    expect(setTok).toHaveBeenLastCalledWith("tok-4", "id-4");
    expect(onDone).toHaveBeenCalledTimes(2);

    // 3) Switching native prop from nativeA -> nativeB while nativeA submit is in flight discards stale callback
    await act(async () => {
      root.render(<PasswordLogin onDone={onDone} native={nativeA} deps={deps} />);
    });
    await act(async () => {
      stateQueue[stateQueue.length - 1]!.resolve(enabledState);
      await Promise.resolve();
    });
    await typeValue("native-pw-a");
    await submitForm();
    const nativeALogin = loginQueue[loginQueue.length - 1]!;

    await act(async () => {
      root.render(<PasswordLogin onDone={onDone} native={nativeB} deps={deps} />);
    });
    await act(async () => {
      nativeALogin.resolve({ code: "stale-native-code" });
      await Promise.resolve();
    });
    expect(assignLocation).not.toHaveBeenCalled();
    expect(host.querySelector("a")).toBeNull();
    expectNoGlobalCalls();
  });

  it("keeps typed password and skips refetch when re-rendered with structurally equal native and new inline deps/onDone", async () => {
    const stateSpy = vi.fn(async () => enabledState);
    const firstOnDone = vi.fn();
    const secondOnDone = vi.fn();
    const setTok = vi.fn();

    await act(async () => {
      root.render(
        <PasswordLogin
          onDone={firstOnDone}
          native={{ ...nativeA }}
          deps={{
            passwordApi: stateSpy,
            loginWithPassword: async () => ({ code: "c1" }),
            setToken: setTok,
            assignLocation: vi.fn(),
          }}
        />,
      );
    });
    await typeValue("kept-pw");
    await act(async () => {
      root.render(
        <PasswordLogin
          onDone={secondOnDone}
          native={{ ...nativeA }}
          deps={{
            passwordApi: stateSpy,
            loginWithPassword: async () => ({ code: "c2" }),
            setToken: setTok,
            assignLocation: vi.fn(),
          }}
        />,
      );
    });
    expect(stateSpy).toHaveBeenCalledOnce();
    expect(inputEl().value).toBe("kept-pw");
    expectNoGlobalCalls();
  });
});

describe("PasswordLogin default fallback compatibility", () => {
  it("delegates to default passwordApi, loginWithPassword, setToken, real default token pairing persistence, and readNativeLogin when deps are omitted", async () => {
    localStorage.setItem(UNREVOKED_KEY, "cred_prev");
    globals.passwordApi.mockResolvedValue(enabledState);
    globals.loginWithPassword.mockResolvedValue({ token: "tok_default", id: "cred_default", hub_id: "hub-1" });
    globals.api.mockImplementation(async (path: string) => (
      path === "/auth/tokens" ? { token: "jd_exchanged", id: "cred_exchanged" } : { machines: [] }
    ));
    globals.readNativeLogin.mockReturnValue(nativeA);

    const onDone = vi.fn();
    await act(async () => {
      root.render(<PasswordLogin onDone={onDone} error="from-link" />);
    });
    expect(globals.passwordApi).toHaveBeenCalledWith("/state");
    expect(alertText()).toBe("from-link");

    await typeValue("default-pw");
    await submitForm();
    expect(globals.loginWithPassword).toHaveBeenCalledWith("default-pw", null, { replaces: false });
    expect(globals.setToken).toHaveBeenCalledWith("tok_default", "cred_default");
    expect(onDone).toHaveBeenCalledOnce();

    await act(async () => modeToggleButton()!.click());
    await typeValue("persona://pair?url=https%3A%2F%2Fhub.example&token=jp_good");
    await submitForm();
    expect(globals.api).toHaveBeenCalledWith(
      "/machines",
      expect.objectContaining({ token: "jp_good", signal: expect.any(AbortSignal) }),
    );
    expect(globals.api).toHaveBeenCalledWith(
      "/auth/tokens",
      expect.objectContaining({
        method: "POST",
        token: "jp_good",
        json: expect.objectContaining({ kind: "web", replaces: "cred_prev" }),
        signal: expect.any(AbortSignal),
      }),
    );
    expect(localStorage.getItem("persona.token")).toBe("jd_exchanged");
    expect(localStorage.getItem("persona.tokenId")).toBe("cred_exchanged");
    expect(localStorage.getItem(UNREVOKED_KEY)).toBeNull();
    expect(onDone).toHaveBeenCalledTimes(2);

    // NativePasswordLogin with no props reads window.location.href via default readNativeLogin
    await act(async () => {
      root.render(<NativePasswordLogin />);
    });
    expect(globals.readNativeLogin).toHaveBeenCalledWith(window.location.href);
  });

  it("does not persist default token pairing credentials or forgetUnrevoked when unmounted before /machines or /auth/tokens settles", async () => {
    localStorage.setItem(UNREVOKED_KEY, "cred_keep");
    globals.passwordApi.mockResolvedValue(enabledState);

    // Phase 1: unmount while /machines is pending (late resolve ignores AbortSignal)
    const machines1 = deferred<{ machines: unknown[] }>();
    let machinesSignal1!: AbortSignal;
    globals.api.mockImplementation((path: string, init?: { signal?: AbortSignal }) => {
      if (path === "/machines") {
        machinesSignal1 = init!.signal!;
        return machines1.promise;
      }
      throw new Error("unexpected /auth/tokens after unmount");
    });

    const onDone = vi.fn();
    await act(async () => {
      root.render(<PasswordLogin onDone={onDone} />);
    });
    await act(async () => modeToggleButton()!.click());
    await typeValue("jp_unmount_machines");
    await submitForm();
    expect(machinesSignal1.aborted).toBe(false);

    await act(async () => root.unmount());
    expect(machinesSignal1.aborted).toBe(true);

    await act(async () => {
      machines1.resolve({ machines: [] });
      await Promise.resolve();
    });
    expect(globals.api).toHaveBeenCalledTimes(1);
    expect(globals.setPairedToken).not.toHaveBeenCalled();
    expect(globals.forgetUnrevoked).not.toHaveBeenCalled();
    expect(localStorage.getItem("persona.token")).toBeNull();
    expect(localStorage.getItem("persona.tokenId")).toBeNull();
    expect(localStorage.getItem(UNREVOKED_KEY)).toBe("cred_keep");
    expect(onDone).not.toHaveBeenCalled();

    // Phase 2: unmount while /auth/tokens exchange is pending (late resolve ignores AbortSignal)
    const tokens2 = deferred<{ token: string; id: string }>();
    let tokensSignal2!: AbortSignal;
    globals.api.mockReset().mockImplementation((path: string, init?: { signal?: AbortSignal }) => {
      if (path === "/machines") return Promise.resolve({ machines: [] });
      tokensSignal2 = init!.signal!;
      return tokens2.promise;
    });

    root = createRoot(host);
    await act(async () => {
      root.render(<PasswordLogin onDone={onDone} />);
    });
    await act(async () => modeToggleButton()!.click());
    await typeValue("jp_unmount_exchange");
    await submitForm();
    expect(tokensSignal2.aborted).toBe(false);

    await act(async () => root.unmount());
    expect(tokensSignal2.aborted).toBe(true);

    await act(async () => {
      tokens2.resolve({ token: "jd_stale_unmount", id: "cred_stale_unmount" });
      await Promise.resolve();
    });
    expect(globals.setPairedToken).not.toHaveBeenCalled();
    expect(globals.forgetUnrevoked).not.toHaveBeenCalled();
    expect(localStorage.getItem("persona.token")).toBeNull();
    expect(localStorage.getItem("persona.tokenId")).toBeNull();
    expect(localStorage.getItem(UNREVOKED_KEY)).toBe("cred_keep");
    expect(onDone).not.toHaveBeenCalled();

    // Phase 3: unmount while old-hub 404 fallback on /auth/tokens is pending must also persist nothing
    const tokens404 = deferred<{ token: string; id: string }>();
    globals.api.mockReset().mockImplementation((path: string) => (
      path === "/machines" ? Promise.resolve({ machines: [] }) : tokens404.promise
    ));

    root = createRoot(host);
    await act(async () => {
      root.render(<PasswordLogin onDone={onDone} />);
    });
    await act(async () => modeToggleButton()!.click());
    await typeValue("master_old_hub");
    await submitForm();
    await act(async () => root.unmount());

    await act(async () => {
      tokens404.reject(new ApiError(404, "not found", {}));
      await Promise.resolve();
    });
    expect(globals.setPairedToken).not.toHaveBeenCalled();
    expect(localStorage.getItem("persona.token")).toBeNull();
    expect(onDone).not.toHaveBeenCalled();
  });

  it("fences delayed default token pairing persistence and busy state across scopeKey ABA switches", async () => {
    localStorage.setItem(UNREVOKED_KEY, "cred_aba_old");
    globals.passwordApi.mockResolvedValue(enabledState);

    const tokenExchanges: Array<{ deferred: ReturnType<typeof deferred<{ token: string; id: string }>>; signal: AbortSignal }> = [];
    globals.api.mockImplementation((path: string, init?: { signal?: AbortSignal }) => {
      if (path === "/machines") return Promise.resolve({ machines: [] });
      const entry = { deferred: deferred<{ token: string; id: string }>(), signal: init!.signal! };
      tokenExchanges.push(entry);
      return entry.deferred.promise;
    });

    const onDone = vi.fn();
    await act(async () => {
      root.render(<PasswordLogin onDone={onDone} scopeKey="hub-a" />);
    });
    await act(async () => modeToggleButton()!.click());
    await typeValue("jp_aba_first");
    await submitForm();
    expect(tokenExchanges).toHaveLength(1);
    expect(tokenExchanges[0]!.signal.aborted).toBe(false);
    expect(submitButton().disabled).toBe(true);

    // Switch scopeKey A -> B -> A while attempt #1's /auth/tokens is still pending
    await act(async () => {
      root.render(<PasswordLogin onDone={onDone} scopeKey="hub-b" />);
    });
    expect(tokenExchanges[0]!.signal.aborted).toBe(true);

    await act(async () => {
      root.render(<PasswordLogin onDone={onDone} scopeKey="hub-a" />);
    });
    await act(async () => modeToggleButton()!.click());
    await typeValue("jp_aba_second");
    await submitForm();
    expect(tokenExchanges).toHaveLength(2);
    expect(submitButton().disabled).toBe(true);

    // Late resolution of stale attempt #1 must NOT persist credentials, clear UNREVOKED_KEY, call onDone, or clear busy
    await act(async () => {
      tokenExchanges[0]!.deferred.resolve({ token: "jd_stale_aba", id: "cred_stale_aba" });
      await Promise.resolve();
    });
    expect(globals.setPairedToken).not.toHaveBeenCalled();
    expect(globals.forgetUnrevoked).not.toHaveBeenCalled();
    expect(localStorage.getItem("persona.token")).toBeNull();
    expect(localStorage.getItem("persona.tokenId")).toBeNull();
    expect(localStorage.getItem(UNREVOKED_KEY)).toBe("cred_aba_old");
    expect(onDone).not.toHaveBeenCalled();
    expect(submitButton().disabled).toBe(true);

    // Active attempt #2 commits credentials and invokes onDone exactly once
    await act(async () => {
      tokenExchanges[1]!.deferred.resolve({ token: "jd_fresh_aba", id: "cred_fresh_aba" });
      await Promise.resolve();
    });
    expect(localStorage.getItem("persona.token")).toBe("jd_fresh_aba");
    expect(localStorage.getItem("persona.tokenId")).toBe("cred_fresh_aba");
    expect(localStorage.getItem(UNREVOKED_KEY)).toBeNull();
    expect(globals.setPairedToken).toHaveBeenCalledExactlyOnceWith("jd_fresh_aba", "cred_fresh_aba");
    expect(onDone).toHaveBeenCalledOnce();
  });

  it("propagates /state replaces capability and resets it across scopeKey changes", async () => {
    const onDone = vi.fn();
    const loginSpy = vi.fn(async () => ({ token: "jd_pw", id: "dev_pw", hub_id: "hub-1" }));
    const passwordApiSpy = vi.fn<() => Promise<PasswordState>>()
      .mockResolvedValueOnce({ ...enabledState, replaces: true })
      .mockResolvedValueOnce(enabledState);
    const deps: PasswordLoginDeps = {
      passwordApi: passwordApiSpy,
      loginWithPassword: loginSpy,
      setToken: vi.fn(),
    };

    await act(async () => {
      root.render(<PasswordLogin onDone={onDone} scopeKey="hub-replaces" deps={deps} />);
    });
    await typeValue("pw-1");
    await submitForm();
    expect(loginSpy).toHaveBeenLastCalledWith("pw-1", null, { replaces: true });

    await act(async () => {
      root.render(<PasswordLogin onDone={onDone} scopeKey="hub-legacy" deps={deps} />);
    });
    await typeValue("pw-2");
    await submitForm();
    expect(loginSpy).toHaveBeenLastCalledWith("pw-2", null, { replaces: false });
    expectNoGlobalCalls();
  });
});
