import { t } from "../i18n";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, errorText, setPairedToken, setToken } from "../lib/api";
import { deviceName, forgetUnrevoked, pairToken, unrevokedTokenId } from "../lib/auth";
import { storedCredentials } from "../lib/compatibility";
import {
  loginWithPassword,
  passwordApi,
  readNativeLogin,
  type NativeLogin,
  type PasswordCredential,
  type PasswordState,
} from "../lib/password";
import { Button, Spinner } from "../ui/atoms";
import { Icon } from "../ui/icons";

export type PasswordLoginMode = "password" | "token";

export interface PasswordLoginDeps {
  passwordApi?: (path: string, json?: unknown) => Promise<PasswordState>;
  loginWithPassword?: (
    password: string,
    native: NativeLogin | null,
    options?: { replaces?: boolean },
  ) => Promise<PasswordCredential | { code: string }>;
  pair?: (input: string, signal?: AbortSignal) => Promise<void>;
  setToken?: (token: string, id?: string) => void;
  formatError?: (error: unknown) => string;
  assignLocation?: (url: string) => void;
}

export interface PasswordLoginProps {
  onDone: () => void;
  error?: string;
  native?: NativeLogin | null;
  scopeKey?: string;
  deps?: PasswordLoginDeps;
}

export interface PasswordLoginViewProps {
  native: NativeLogin | null;
  mode: PasswordLoginMode;
  passwordEnabled: boolean;
  loading: boolean;
  busy: boolean;
  token: string;
  error: string;
  appCallback: string;
  onTokenChange: (value: string) => void;
  onSubmit: (e: React.FormEvent) => void;
  onToggleMode: () => void;
}

export interface NativePasswordLoginDeps extends PasswordLoginDeps {
  readNativeLogin?: (href: string) => NativeLogin | null;
  getHref?: () => string;
}

export interface NativePasswordLoginProps {
  href?: string;
  onDone?: () => void;
  scopeKey?: string;
  deps?: NativePasswordLoginDeps;
}

const noop = () => {};
const defaultAssignLocation = (url: string) => window.location.assign(url);

// Mirrors lib/auth.ts pair(), but verifies `isCurrent()` and `signal` before committing global credentials so an
// unmounted or superseded login view cannot silently persist auth while dropping `onDone()`.
async function defaultPair(input: string, signal: AbortSignal, isCurrent: () => boolean): Promise<boolean> {
  const tok = pairToken(input);
  await api("/machines", { token: tok, signal });
  if (signal.aborted || !isCurrent()) return false;
  let use = tok;
  let tokenId: string | undefined;
  let exchanged = false;
  if (!tok.startsWith("jd_")) {
    try {
      const replaces = storedCredentials().tokenId || unrevokedTokenId();
      const json = { name: deviceName(), kind: "web", ...(replaces ? { replaces } : {}) };
      const r = await api<{ token: string; id: string }>("/auth/tokens", { method: "POST", json, token: tok, signal });
      if (signal.aborted || !isCurrent()) return false;
      use = r.token;
      tokenId = r.id;
      exchanged = true;
    } catch (err) {
      if (signal.aborted || !isCurrent()) return false;
      if (!(err instanceof ApiError) || err.status !== 404) throw err;
    }
  }
  if (signal.aborted || !isCurrent()) return false;
  if (exchanged) forgetUnrevoked(); // the hub has had its one chance at it
  if (tokenId === undefined) setPairedToken(use);
  else setPairedToken(use, tokenId);
  return true;
}

/** Pure presentational view for connection-password and token sign-in. */
export function PasswordLoginView({
  native, mode, passwordEnabled, loading, busy, token, error, appCallback, onTokenChange, onSubmit, onToggleMode,
}: PasswordLoginViewProps) {
  return (
    <div className="flex min-h-full items-center justify-center bg-page px-5 py-16">
      <form onSubmit={onSubmit} className="w-full max-w-[360px]" style={{ animation: "fade-up 450ms cubic-bezier(0.23,1,0.32,1) both" }}>
        <div className="mb-7 flex flex-col items-center gap-3 text-center">
          <span className="flex size-11 items-center justify-center rounded-[12px] bg-ink text-surface shadow-raised">
            <Icon name="spark" size={20} />
          </span>
          <div>
            <h1 className="text-[18px] font-semibold tracking-tight text-ink">{t("password.title")}</h1>
            <p className="mt-1 text-[13px] leading-relaxed text-ink-2">{native ? t("password.native_prompt", { name: native.name }) : mode === "password" ? t("password.prompt") : t("password.token_prompt")}</p>
          </div>
        </div>
        {appCallback ? <div className="rounded-card bg-surface p-4 text-center shadow-card">
          <p className="mb-3 text-[13px] text-ink" role="status">{t("password.success")}</p>
          <a href={appCallback} className="inline-flex h-9 items-center justify-center rounded-control bg-ink px-5 text-[13px] text-surface">{t("password.return_app")}</a>
        </div> : <div className="rounded-card bg-surface p-1.5 shadow-card">
          <label className="flex h-10 items-center gap-2 rounded-control bg-inset px-3 shadow-hairline focus-within:shadow-[0_0_0_1px_var(--line-strong)]">
            <Icon name="key" size={14} className="text-ink-3" />
            <input
              autoFocus
              type="password"
              value={token}
              onChange={(e) => onTokenChange(e.target.value)}
              placeholder={mode === "password" ? t("password.connection_password") : "hub token"}
              aria-label={mode === "password" ? t("password.connection_password") : "hub token"}
              autoComplete={mode === "password" ? "current-password" : "off"}
              disabled={loading || busy || Boolean(native && !passwordEnabled)}
              className="min-w-0 flex-1 bg-transparent font-mono text-[13px] text-ink outline-none placeholder:font-sans placeholder:text-ink-3"
            />
          </label>
          <Button type="submit" tone="primary" disabled={loading || busy || !token || Boolean(native && !passwordEnabled)} className="mt-1.5 h-9 w-full">
            {busy || loading ? <Spinner /> : t("password.connect")}
          </Button>
        </div>}
        <p className="mt-3 h-5 text-center text-[12.5px] text-red" role="alert">
          {error}
        </p>
        {!native && passwordEnabled && <button type="button" className="mb-4 w-full text-center text-[12.5px] text-ink-2" disabled={busy} onClick={onToggleMode}>
          {mode === "password" ? t("password.use_token") : t("password.use_password")}
        </button>}
        <p className="text-center text-[11.5px] leading-relaxed text-ink-3">{native ? t("password.native_credential") : t("password.browser_credential")}</p>
      </form>
    </div>
  );
}

// Sign in with a connection password or pair with a hub or device token. `linkError` reports an invalid token from a link.
export function PasswordLogin({ onDone, error: linkError = "", native = null, scopeKey, deps }: PasswordLoginProps) {
  const scopeId = JSON.stringify([
    scopeKey ?? null,
    native ? [native.state, native.challenge, native.kind, native.name] : null,
  ]);
  const [scope, setScope] = useState<{ id: string; epoch: number }>(() => ({ id: scopeId, epoch: 1 }));
  const [prevLinkError, setPrevLinkError] = useState(linkError);
  const [token, setTok] = useState("");
  const [passwordEnabled, setPasswordEnabled] = useState(false);
  const [supportsReplaces, setSupportsReplaces] = useState(false);
  const [mode, setMode] = useState<PasswordLoginMode>("password");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(linkError);
  const [appCallback, setAppCallback] = useState("");

  if (scope.id !== scopeId) {
    setScope({ id: scopeId, epoch: scope.epoch + 1 });
    setPrevLinkError(linkError);
    setTok("");
    setPasswordEnabled(false);
    setSupportsReplaces(false);
    setMode("password");
    setLoading(true);
    setBusy(false);
    setError(linkError);
    setAppCallback("");
  } else if (prevLinkError !== linkError) {
    setPrevLinkError(linkError);
    setError(linkError);
  }

  const onDoneRef = useRef(onDone);
  const depsRef = useRef(deps);
  const nativeRef = useRef(native);
  useLayoutEffect(() => {
    onDoneRef.current = onDone;
    depsRef.current = deps;
    nativeRef.current = native;
  });

  const activeEpochRef = useRef(0);
  const submitSeqRef = useRef(0);
  const pendingSubmitRef = useRef<{ epoch: number; seq: number; controller: AbortController } | null>(null);

  useLayoutEffect(() => {
    activeEpochRef.current = scope.epoch;
    return () => {
      activeEpochRef.current = 0;
      pendingSubmitRef.current?.controller.abort();
      pendingSubmitRef.current = null;
      submitSeqRef.current += 1;
    };
  }, [scope.epoch]);

  useEffect(() => {
    let live = true;
    const runEpoch = scope.epoch;
    const runNative = nativeRef.current;
    const readState = depsRef.current?.passwordApi ?? passwordApi<PasswordState>;
    const isCurrent = () => live && activeEpochRef.current === runEpoch;
    let request: Promise<PasswordState>;
    try {
      request = Promise.resolve(readState("/state"));
    } catch (err) {
      request = Promise.reject(err);
    }
    request.then((state) => {
      if (!isCurrent()) return;
      setPasswordEnabled(state.enabled);
      setSupportsReplaces(Boolean(state.replaces));
      if (!state.enabled) {
        setMode("token");
        if (runNative) setError(t("password.disabled"));
      }
    }, (err) => {
      if (!isCurrent()) return;
      setMode("token");
      if (runNative) setError((depsRef.current?.formatError ?? errorText)(err));
    }).finally(() => {
      if (isCurrent()) setLoading(false);
    });
    return () => { live = false; };
  }, [scope.epoch]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const runEpoch = scope.epoch;
    if (busy || loading || appCallback || pendingSubmitRef.current !== null || activeEpochRef.current !== runEpoch) return;
    const runMode = mode;
    const runNative = nativeRef.current;
    const credential = runMode === "password" ? token : token.trim();
    if (!credential) return;
    const controller = new AbortController();
    const runSeq = ++submitSeqRef.current;
    pendingSubmitRef.current = { epoch: runEpoch, seq: runSeq, controller };
    const isCurrent = () =>
      !controller.signal.aborted && activeEpochRef.current === runEpoch && pendingSubmitRef.current?.seq === runSeq;
    setBusy(true);
    setError("");
    try {
      if (runMode === "password") {
        const runLogin = depsRef.current?.loginWithPassword ?? loginWithPassword;
        setTok("");
        const result = await runLogin(credential, runNative, { replaces: supportsReplaces });
        if (!isCurrent()) return;
        if (runNative) {
          if (!("code" in result)) throw new Error(t("password.invalid_native_result"));
          const callback = `persona://password-login?${new URLSearchParams({ code: result.code, state: runNative.state })}`;
          setAppCallback(callback);
          (depsRef.current?.assignLocation ?? defaultAssignLocation)(callback);
        } else {
          if (!("token" in result)) throw new Error(t("password.invalid_result"));
          (depsRef.current?.setToken ?? setToken)(result.token, result.id);
          onDoneRef.current();
        }
      } else {
        if (depsRef.current?.pair) {
          await depsRef.current.pair(credential, controller.signal);
          if (!isCurrent()) return;
        } else if (!(await defaultPair(credential, controller.signal, isCurrent))) {
          return;
        }
        onDoneRef.current();
      }
    } catch (err) {
      if (!isCurrent()) return;
      const msg = (depsRef.current?.formatError ?? errorText)(err);
      setError(msg === "invalid token" ? t("password.invalid_token") : msg);
    } finally {
      if (pendingSubmitRef.current?.seq === runSeq) pendingSubmitRef.current = null;
      if (activeEpochRef.current === runEpoch && submitSeqRef.current === runSeq) setBusy(false);
    }
  };

  const toggleMode = () => {
    if (busy || pendingSubmitRef.current !== null) return;
    setMode((m) => (m === "password" ? "token" : "password"));
    setTok("");
    setError("");
  };

  return (
    <PasswordLoginView
      native={native}
      mode={mode}
      passwordEnabled={passwordEnabled}
      loading={loading}
      busy={busy}
      token={token}
      error={error}
      appCallback={appCallback}
      onTokenChange={setTok}
      onSubmit={submit}
      onToggleMode={toggleMode}
    />
  );
}

export function NativePasswordLogin({ href, onDone = noop, scopeKey, deps }: NativePasswordLoginProps = {}) {
  const read = deps?.readNativeLogin ?? readNativeLogin;
  const [initialHref] = useState(() => (href !== undefined ? href : deps?.getHref ? deps.getHref() : window.location.href));
  const currentHref = href !== undefined ? href : initialHref;
  const request = useMemo(() => {
    try { return { native: read(currentHref), error: "" }; }
    catch { return { native: null, error: t("password.invalid_request_full") }; }
  }, [read, currentHref]);
  if (!request.native) return <p role="alert" className="p-8 text-ink">{request.error || t("password.start_from_app")}</p>;
  return <PasswordLogin native={request.native} onDone={onDone} scopeKey={scopeKey} deps={deps} />;
}
