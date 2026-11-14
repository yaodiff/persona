// The live screen of a Mac (docs/extensions/computer-use.md "Live view"): macOS's Screen Sharing on its 127.0.0.1:5900, through the
// hub's port relay, drawn by noVNC. View only until "Take control"; Screen Sharing asks for the Mac account's name and password
// (Apple's ARD sign-in), which go to the Mac only and are not kept. The app shows this page in its web view.
import { BASE } from "../lib/base";
import RFB from "@novnc/novnc";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, errorText, pastHub } from "../lib/api";
import { cx, Spinner } from "../ui/atoms";
import { t } from "../i18n";
import "../views/theme"; // follows the system's light / dark while open (index.html picked the first one)
import { ensureSubtleAES } from "./aes";
import { displayChoice, framing, keepPointerIn, layoutDisplays, machineDisplays, setDisplayChoice, type MachineDisplay, type Rect, type Size } from "./displays";
import { accountOf, keepLabel, keysymsOf, screenSocket, toApp, type ScreenTarget } from "./screen";
import { newPair } from "./pair";
import { pictureChoice, qualityChoice, setPictureChoice, setQualityChoice, setStatsShown, statsShown, videoOffered, webTiers, type ScreenPicture, type ScreenQuality } from "./video";
import type { Want } from "./video-link";
import { budgetOf, chooseMobileData, currentMobileData, markMobileDataAsked, onMobileData, readNetwork, shouldAskMobileData, watchNetwork, type MobileData } from "./data-use";
import type { Probe } from "./video-probe";

type DisplayMenuView = typeof import("./DisplayMenu").default;
type ScreenMenuView = typeof import("./ScreenMenu").default;
type StatsKit = typeof import("./StatsOverlay");
type VideoKit = typeof import("./VideoPicture");
type VideoState = import("./VideoPicture").VideoState;

type Phase =
  | { kind: "checking" }
  | { kind: "blocked"; why: string }
  | { kind: "connecting" }
  | { kind: "signin"; failed?: string }
  | { kind: "live" }
  | { kind: "closed"; why: string };

type Computer = { enabled?: boolean; screen_sharing?: boolean; displays?: unknown; screen_video?: { permission?: string; asked?: boolean } | unknown };
/** A sign-in sent to the Mac: typed (and to keep or not), or the one the app kept (saved). */
type Credentials = { account: string; password: string; remember: boolean; saved: boolean };
type MachineRow = { id: string; name: string; home?: string; online: boolean };

export function ScreenView({ target }: { target: ScreenTarget }) {
  const [phase, setPhase] = useState<Phase>({ kind: "checking" });
  const [name, setName] = useState(target.name);
  const [account, setAccount] = useState("");
  const [password, setPassword] = useState("");
  const [control, setControl] = useState(false);
  const controlRef = useRef(false);
  controlRef.current = control;
  const [attempt, setAttempt] = useState(0);
  // The app keeps the Mac's sign-in (#1322): "Remember on this phone", on unless unticked.
  const canKeep = target.embedded && target.keep !== "";
  const [remember, setRemember] = useState(true);
  const queued = useRef<Credentials | null>(null); // typed while there was no connection waiting: sent on the next
  const sent = useRef<Credentials | null>(null); // sent on this connection, until the Mac says yes or no
  const waiting = useRef(false); // this connection asked for a sign-in and has none yet
  const answer = useRef<((c: Credentials) => void) | null>(null);
  const unlockAsked = useRef(false); // one-tap unlock asked for its password once per page
  // Set once the hub says there is a screen to connect to: the connection lives from then on, whatever the page
  // shows (asking to sign in is part of it), until "Reconnect" or the page goes.
  const [go, setGo] = useState(false);
  const screen = useRef<HTMLDivElement>(null);
  const rfb = useRef<RFB | null>(null);
  // One display of a multi-display Mac (#5685): its arrangement as the node read it, the framebuffer's size as noVNC
  // sized its canvas, the room the page has for it, and the display chosen for this Mac (null: all of them).
  const [displays, setDisplays] = useState<MachineDisplay[]>([]);
  const [fb, setFb] = useState<Size | null>(null);
  const [stage, setStage] = useState<Size | null>(null);
  const [choice, setChoice] = useState(() => displayChoice(target.mid));
  const stageRef = useRef<HTMLElement>(null);
  const clip = useRef<HTMLDivElement>(null);
  // The hardware video channel (#3954): the Picture / Quality switcher appears only when this Mac's node advertises it,
  // so no Mac shows a control it can't use — as the iOS app gates it (AppModel.advertisesVideo). The choice is kept per
  // Mac in this browser (video.ts). Input always rides RFB; only the picture path changes.
  const [hasVideo, setHasVideo] = useState(false);
  const hasVideoRef = useRef(false);
  hasVideoRef.current = hasVideo;
  // The video channel itself (#6558): whether the Mac allowed it (`permission`, and whether macOS has asked yet), what
  // this browser decodes, and the channel's state; the pair that binds it to the RFB connection once that is signed in.
  const [videoPermission, setVideoPermission] = useState<{ missing: boolean; asked: boolean }>({ missing: false, asked: false });
  const [probe, setProbe] = useState<Probe | null>(null);
  const [video, setVideo] = useState<VideoState | null>(null);
  const [livePair, setLivePair] = useState<string | null>(null);
  // The choice kept for this Mac, null until one is made: video then by default where it plays (`playable`), as on the
  // iPhone; VNC otherwise.
  const [pictureKept, setPicture] = useState<ScreenPicture | null>(() => pictureChoice(target.mid));
  const [quality, setQuality] = useState<ScreenQuality>(() => qualityChoice(target.mid));
  // Connection stats (#6558): off until turned on, kept per Mac (video.ts). The overlay measures this connection: noVNC
  // and the RFB WebSocket the page handed it, while it is shown.
  const [statsOn, setStatsOn] = useState(() => statsShown(target.mid));
  const [link, setLink] = useState<{ rfb: RFB; socket: WebSocket } | null>(null);

  // What the hub says first: why there would be nothing to connect to.
  useEffect(() => {
    let gone = false;
    (async () => {
      if (!target.token) return setPhase({ kind: "blocked", why: t("screen-screenview.sign_in_first") });
      try {
        const { machines } = await api<{ machines: MachineRow[] }>("/machines", { token: target.token });
        const m = machines.find((x) => x.id === target.mid);
        if (m) {
          setName(m.name);
          setAccount((a) => a || accountOf(m.home));
        }
        if (m && !m.online) throw new Error(t("screen-screenview.offline", { name: m.name }));
        const on = await sharing(target);
        if (!gone) {
          setDisplays(machineDisplays(on.displays));
          setHasVideo(videoOffered(on.screen_video));
          const sv = (on.screen_video ?? {}) as { permission?: string; asked?: boolean };
          setVideoPermission({ missing: sv.permission === "missing", asked: sv.asked === true });
          setPhase({ kind: "connecting" });
          setGo(true);
        }
      } catch (e) {
        const why = e instanceof ApiError && e.status === 404 ? t("screen-screenview.not_found")
          : e instanceof ApiError && e.status === 401 ? (target.embedded ? t("screen-screenview.unauthorized_app") : t("screen-screenview.unauthorized_web"))
          : errorText(e);
        if (!gone) setPhase({ kind: "blocked", why });
      }
    })();
    return () => {
      gone = true;
    };
  }, [target]);

  // The connection, once there is something to connect to; again on "Reconnect".
  useEffect(() => {
    if (!go || !screen.current) return;
    ensureSubtleAES(); // an http page has no crypto.subtle, and the ARD sign-in needs AES
    // The page opens the RFB WebSocket and hands it to noVNC (its constructor takes a channel), so the connection stats
    // can count what it carries; it is the socket noVNC would open itself, the same URL with no subprotocols. noVNC
    // closes it when it disconnects.
    // A Mac that offers video gets a pair on its RFB connection: the video channel binds to this one once it signs in.
    const pair = hasVideoRef.current ? newPair() : undefined;
    const socket = new WebSocket(screenSocket(window.location, target.mid, target.token) + (pair ? `&pair=${pair}` : ""));
    let r: RFB;
    try {
      r = new RFB(screen.current, socket, { shared: true });
    } catch (e) {
      socket.close();
      throw e;
    }
    r.viewOnly = true;
    r.scaleViewport = true;
    r.resizeSession = false;
    r.background = "transparent";
    // noVNC sizes its canvas to the framebuffer (and again when the Mac's displays change); nothing else says the size.
    const canvas = screen.current.querySelector("canvas");
    const sized = new MutationObserver(() => {
      const w = canvas?.width ?? 0, h = canvas?.height ?? 0;
      if (w > 0 && h > 0) setFb((f) => (f?.w === w && f.h === h ? f : { w, h }));
    });
    if (canvas) sized.observe(canvas, { attributes: true, attributeFilter: ["width", "height"] });
    waiting.current = false;
    sent.current = null;
    let askedApp = false;
    let appSilent: ReturnType<typeof setTimeout> | undefined;
    const send = (c: Credentials) => {
      waiting.current = false;
      sent.current = c;
      r.sendCredentials({ username: c.account, password: c.password });
      setPhase({ kind: "connecting" });
    };
    const showForm = () => setPhase((p) => (p.kind === "signin" ? p : { kind: "signin" }));
    answer.current = (c) => {
      clearTimeout(appSilent);
      if (waiting.current) send(c);
    };
    r.addEventListener("connect", () => {
      setPhase({ kind: "live" });
      if (pair) setLivePair(pair);
      // One-tap unlock (#1507 a-2): in, so the app hands over the kept password for the lock screen (personaScreen.unlock).
      if (target.unlock && !unlockAsked.current) {
        unlockAsked.current = true;
        toApp({ credentials: "unlock" });
      }
      const c = sent.current;
      sent.current = null;
      // What worked goes to the app's keychain when "Remember" was on; off, what it kept goes.
      if (c && canKeep && !c.saved) toApp(c.remember ? { credentials: "worked", account: c.account, password: c.password } : { credentials: "forget" });
    });
    r.addEventListener("credentialsrequired", () => {
      waiting.current = true;
      const q = queued.current;
      queued.current = null;
      if (q) return send(q);
      if (canKeep && !askedApp) {
        // The app answers with what it kept (personaScreen.signIn), or null for the form; the form if it says nothing.
        askedApp = true;
        appSilent = setTimeout(showForm, 1500);
        return toApp({ credentials: "wanted" });
      }
      showForm();
    });
    r.addEventListener("securityfailure", (e) => {
      const reason = (e as CustomEvent<{ reason?: string }>).detail?.reason;
      const kept = sent.current?.saved ? sent.current : null;
      sent.current = null;
      if (kept) setAccount(kept.account);
      setPhase({
        kind: "signin",
        failed: kept ? t("screen-screenview.kept_password_wrong") : reason ? t("screen-screenview.sign_in_failed", { reason }) : t("screen-screenview.wrong_credentials"),
      });
    });
    r.addEventListener("disconnect", (e) => {
      const clean = (e as CustomEvent<{ clean: boolean }>).detail?.clean;
      setPhase((p) => (p.kind === "signin" && p.failed ? p : { kind: "closed", why: clean ? t("screen-screenview.disconnected") : t("screen-screenview.connection_lost") }));
    });
    rfb.current = r;
    setLink({ rfb: r, socket });
    return () => {
      sized.disconnect();
      clearTimeout(appSilent);
      answer.current = null;
      rfb.current = null;
      setLink(null);
      setLivePair(null);
      r.disconnect();
    };
  }, [go, attempt, target, canKeep]);

  useEffect(() => {
    if (rfb.current) rfb.current.viewOnly = !control;
  }, [control, phase.kind]);

  // The room for the picture: the page below its bar.
  useLayoutEffect(() => {
    const el = stageRef.current;
    if (!el) return;
    const measure = () => {
      const { width: w, height: h } = el.getBoundingClientRect();
      setStage((s) => (s?.w === w && s.h === h ? s : { w, h }));
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const watch = new ResizeObserver(measure);
    watch.observe(el);
    return () => watch.disconnect();
  }, []);

  // The arrangement the node reads now; the latest answer wins. Asked again when the framebuffer changes size (a display
  // plugged in or out, another resolution: the one read before connecting no longer fits) and as the menu opens, so
  // its list is the Mac's as the choice is made.
  const asked = useRef(0);
  const reask = useCallback(() => {
    const n = ++asked.current;
    sharing(target).then(
      (on) => n === asked.current && setDisplays(machineDisplays(on.displays)),
      () => {}, // the arrangement already here stays; the layout offers nothing when it does not fit
    );
  }, [target]);
  useEffect(() => () => void asked.current++, []); // an answer after the page went changes nothing
  const lastFb = useRef<Size | null>(null);
  useEffect(() => {
    if (!fb) return;
    const before = lastFb.current;
    lastFb.current = fb;
    if (before && (before.w !== fb.w || before.h !== fb.h)) reask();
  }, [fb, reask]);

  const offered = useMemo(() => (fb ? layoutDisplays(displays, fb) : []), [displays, fb]);
  const offering = !target.embedded && phase.kind === "live" && offered.length > 1;
  // The menu is a chunk of its own (its words stay off the first screen's catalog), fetched while a Mac with several
  // displays connects, so a kept display is cropped from the first live frame. One that would not load (offline, the hub
  // redeployed) leaves the page showing every display, and is asked for again as the connection moves on.
  const multiDisplay = !target.embedded && displays.length > 1;
  const [DisplayMenu, setDisplayMenu] = useState<DisplayMenuView | null>(null);
  // One display only while its menu is there to go back to all of them: a page an app embeds has no bar to choose in,
  // and a menu that would not load leaves none.
  const shown = DisplayMenu ? (offered.find((d) => d.id === choice) ?? null) : null;
  useEffect(() => {
    if (DisplayMenu || !multiDisplay) return;
    let gone = false;
    import("./DisplayMenu").then(
      (m) => !gone && setDisplayMenu(() => m.default),
      () => {},
    );
    return () => {
      gone = true;
    };
  }, [DisplayMenu, multiDisplay, phase.kind]);
  const frame = shown && fb && stage ? framing(stage, fb, shown.frame) : null;
  const cropped = frame !== null;
  useEffect(() => (cropped && clip.current ? keepPointerIn(clip.current) : undefined), [cropped]);
  const choose = (id: string | null) => {
    setChoice(id);
    setDisplayChoice(target.mid, id);
  };

  // The picture menu is a chunk of its own, as the Displays menu is (its words stay off the first screen's catalog),
  // fetched only for a Mac whose node advertises the video channel; one that would not load just leaves the page on RFB.
  const [ScreenMenu, setScreenMenu] = useState<ScreenMenuView | null>(null);
  useEffect(() => {
    if (ScreenMenu || target.embedded || !hasVideo) return;
    let gone = false;
    import("./ScreenMenu").then(
      (m) => !gone && setScreenMenu(() => m.default),
      () => {},
    );
    return () => {
      gone = true;
    };
  }, [ScreenMenu, hasVideo, target.embedded]);
  const choosePicture = (p: ScreenPicture) => {
    setPicture(p);
    setPictureChoice(target.mid, p);
  };
  const chooseQuality = (q: ScreenQuality) => {
    setQuality(q);
    setQualityChoice(target.mid, q);
  };

  // The connection stats' toggle and overlay are a chunk of their own too (their words in screen-video), fetched once the
  // screen is live in a page with a bar; one that would not load leaves the bar without the toggle.
  const [Stats, setStats] = useState<StatsKit | null>(null);
  const statsWanted = phase.kind === "live" && !target.embedded;
  useEffect(() => {
    if (Stats || !statsWanted) return;
    let gone = false;
    import("./StatsOverlay").then(
      (m) => !gone && setStats(m),
      () => {},
    );
    return () => {
      gone = true;
    };
  }, [Stats, statsWanted]);
  const showStats = (on: boolean) => {
    setStatsOn(on);
    setStatsShown(target.mid, on);
  };

  // The video channel's chunk (#6558), fetched only for a Mac that offers video, in a page with a bar; it first asks
  // what this browser decodes. One that would not load leaves the page on RFB with nothing said.
  const [Video, setVideoKit] = useState<VideoKit | null>(null);
  useEffect(() => {
    if (Video || target.embedded || !hasVideo) return;
    let gone = false;
    import("./VideoPicture").then(
      async (m) => {
        const p = await m.probeHere();
        if (!gone) {
          setProbe(p);
          setVideoKit(m);
        }
      },
      () => {},
    );
    return () => {
      gone = true;
    };
  }, [Video, hasVideo, target.embedded]);
  const playable = hasVideo && !!probe?.codecs.length && !video?.decoderFailed;
  const picture: ScreenPicture = pictureKept ?? (playable ? "video" : "vnc");
  // What video shows: video chosen, the Mac allowed it, and one display: its only one, or one chosen of several the node
  // numbered (`display-<id>`); all of several at once stay RFB, since video carries one.
  const want = useMemo<Want | null>(() => {
    if (!playable || picture !== "video" || videoPermission.missing) return null;
    if (offered.length <= 1) return { display: null, rect: null };
    if (!shown?.id.startsWith("display-")) return null;
    const id = Number(shown.id.slice("display-".length));
    return Number.isFinite(id) ? { display: id, rect: [shown.frame.x, shown.frame.y, shown.frame.w, shown.frame.h] } : null;
  }, [playable, picture, videoPermission.missing, offered.length, shown]);
  // Why VNC shows while video was chosen; nothing while VNC isn't live (the page says so itself).
  const fallback = phase.kind === "live" && playable && picture === "video" ? (videoPermission.missing ? "notAllowed" : (video?.fallback ?? null)) : null;
  // Why video isn't playing on this page although the Mac offers it: the stats panel says so, only then.
  const noVideo = !hasVideo ? null : probe?.why === "insecure" ? "insecure" : probe?.why === "unsupported" ? "unsupported" : video?.decoderFailed ? "failed" : null;

  // How much data the video may use (./data-use, the iPhone's rules): the network as the browser tells it, and with
  // tiers on both sides the choice for mobile data, asked on the card at most twice while nothing is chosen (once a
  // page); the choice also sits in ⋯ while it applies.
  const [net, setNet] = useState(readNetwork);
  useEffect(() => watchNetwork(() => setNet(readNetwork())), []);
  const tiersOn = webTiers.supported && !!video?.tiers;
  const [mobileData, setMobileData] = useState<MobileData>(currentMobileData);
  // The cap without tiers starts the stream; the tiers, once the helper lists them, work under the cap with the choice.
  const budget = useMemo(() => budgetOf(net, null), [net]);
  const tierBudget = useMemo(() => budgetOf(net, mobileData), [net, mobileData]);
  const dataChoice = tiersOn && onMobileData(net);
  const [askData, setAskData] = useState(false);
  const askedHere = useRef(false);
  useEffect(() => {
    if (!dataChoice || askedHere.current) return;
    askedHere.current = true;
    if (shouldAskMobileData()) {
      markMobileDataAsked();
      setAskData(true);
    }
  }, [dataChoice]);
  const chooseData = (m: MobileData) => {
    chooseMobileData(m);
    setMobileData(m);
    setAskData(false);
  };

  // In the app (#1343) the app draws the name, the state and "View only" / "Take control" around the page: it hears the phase, and
  // says "Take control" through window.personaScreen.
  useEffect(() => {
    if (!target.embedded) return;
    toApp({ phase: phase.kind });
  }, [target.embedded, phase.kind]);
  useEffect(() => {
    if (!target.embedded) return;
    (window as unknown as { personaScreen?: unknown }).personaScreen = {
      setControl: (on: boolean) => setControl(on),
      // The app's answer to { credentials: "unlock" }: a key wakes the lock screen and puts the cursor in its password
      // field, then the password and Return; "View only" again after.
      unlock: (kept: { password?: string } | null) => {
        const r = rfb.current;
        if (!r || !kept?.password) return;
        const password = kept.password;
        r.viewOnly = false;
        r.sendKey(0xffe1, "ShiftLeft");
        setTimeout(() => {
          for (const k of keysymsOf(password)) r.sendKey(k, null);
          r.viewOnly = !controlRef.current;
        }, 800);
      },
      // The app's answer to { credentials: "wanted" }: the Mac's sign-in it kept, or null (the form).
      signIn: (kept: { account?: string; password?: string } | null) => {
        if (kept?.account && kept.password) {
          setAccount(kept.account);
          answer.current?.({ account: kept.account, password: kept.password, remember: true, saved: true });
        } else {
          answer.current = null;
          setPhase((p) => (p.kind === "signin" ? p : { kind: "signin" }));
        }
      },
    };
  }, [target.embedded]);

  const signIn = useCallback(
    (e: React.FormEvent) => {
      e.preventDefault();
      const c: Credentials = { account: account.trim(), password, remember: canKeep && remember, saved: false };
      setPassword("");
      setPhase({ kind: "connecting" });
      if (rfb.current && waiting.current) {
        waiting.current = false;
        sent.current = c;
        rfb.current.sendCredentials({ username: c.account, password: c.password });
        return;
      }
      // A refused sign-in closes the connection: a new one takes this when it asks.
      queued.current = c;
      setAttempt((n) => n + 1);
    },
    [account, password, canKeep, remember],
  );

  const reconnect = () => {
    setAttempt((n) => n + 1);
    setPhase({ kind: "connecting" });
  };

  const live = phase.kind === "live";
  return (
    <div className={cx("flex h-dvh flex-col text-ink", target.embedded ? "bg-transparent" : "bg-canvas")}>
      {!target.embedded && (
      <header className="flex h-11 shrink-0 items-center gap-2 border-b border-line bg-surface px-3">
        {!target.embedded && (
          <a href={`${BASE}/machines/${encodeURIComponent(target.mid)}`} className="whitespace-nowrap rounded-control px-2 py-1 text-[12.5px] text-ink-2 hover:bg-hover hover:text-ink">
            {t("screen-screenview.back")}
          </a>
        )}
        <p className="min-w-0 flex-1 truncate text-[13px] font-medium">
          {t("screen-screenview.title", { name })}
          <span className="ml-2 text-[11.5px] font-normal text-ink-3">{live ? (control ? t("screen-screenview.in_control") : t("screen-screenview.view_only")) : phaseLabel(phase)}</span>
        </p>
        {offering && DisplayMenu && <DisplayMenu offered={offered} shown={shown?.id ?? null} onChoose={choose} onOpen={reask} />}
        {live && !target.embedded && playable && ScreenMenu && (
          <ScreenMenu
            picture={picture}
            quality={quality}
            onPicture={choosePicture}
            onQuality={chooseQuality}
            qualityOffered={tiersOn}
            mobileData={dataChoice ? mobileData : null}
            onMobileData={chooseData}
          />
        )}
        {live && Stats && <Stats.StatsToggle on={statsOn} onToggle={showStats} />}
        {live && (
          <div role="group" aria-label={t("screen-screenview.mode_label")} className="flex shrink-0 rounded-control bg-field p-0.5">
            {[false, true].map((on) => (
              <button
                key={String(on)}
                type="button"
                aria-pressed={control === on}
                onClick={() => setControl(on)}
                className={cx(
                  "h-7 whitespace-nowrap rounded-[7px] px-3 text-[12px] font-medium transition-colors",
                  control === on ? "bg-surface text-ink shadow-btn" : "text-ink-2 hover:text-ink",
                )}
              >
                {on ? t("screen-screenview.take_control") : t("screen-screenview.view_only")}
              </button>
            ))}
          </div>
        )}
      </header>
      )}
      <main ref={stageRef} className="relative min-h-0 flex-1">
        {/* One display: a box as large as fits shows it, over the whole framebuffer drawn at the same scale (displays.ts). */}
        <div ref={clip} className={cx("absolute", frame ? "overflow-hidden" : "inset-0", !live && "invisible")} style={frame ? place(frame.clip) : undefined}>
          <div ref={screen} className={cx("absolute", !frame && "inset-0")} style={frame ? place(frame.inner) : undefined} data-testid="screen" />
        </div>
        {!target.embedded && Video && playable && link && (
          <Video.VideoChannel
            rfb={link.rfb}
            screen={screen.current}
            pair={livePair}
            want={want}
            codecs={probe!.codecs}
            budget={budget}
            tierBudget={tierBudget}
            quality={quality}
            tiers={webTiers.supported}
            mid={target.mid}
            token={target.token}
            onState={setVideo}
          />
        )}
        {/* under the bar, centered: why VNC shows, and the mobile-data question; the strip between lets the picture take clicks */}
        {!target.embedded && Video && (fallback || (askData && dataChoice && live && picture === "video")) && (
          <div className="pointer-events-none absolute top-3 right-3 left-3 z-10 flex flex-col items-center gap-2">
            {fallback && (
              <div className="pointer-events-auto">
                <Video.FallbackLine fallback={fallback} asked={videoPermission.asked} />
              </div>
            )}
            {askData && dataChoice && live && picture === "video" && <Video.MobileDataCard onChoose={chooseData} onClose={() => setAskData(false)} />}
          </div>
        )}
        {live && !target.embedded && statsOn && Stats && link && (
          <Stats.StatsOverlay
            rfb={link.rfb}
            socket={link.socket}
            onClose={() => showStats(false)}
            video={video?.showing ? { reading: video.reading, codec: video.codec, size: video.size, tier: tiersOn && video.tier ? { change: video.tier, at: video.tierAt ?? 0, quality } : null } : null}
            noVideo={noVideo}
          />
        )}
        {!live && (
          <div className="absolute inset-0 flex items-center justify-center p-6">
            {phase.kind === "signin" ? (
              <form onSubmit={signIn} className="flex w-full max-w-[340px] flex-col gap-3 rounded-card bg-surface p-5 shadow-card">
                <div>
                  <p className="text-[14px] font-medium">{t("screen-screenview.signin_title")}</p>
                  <p className="mt-1 text-[12.5px] text-ink-2">
                    {canKeep && remember ? t("screen-screenview.signin_hint_kept", { name }) : t("screen-screenview.signin_hint", { name })}
                  </p>
                </div>
                {phase.failed && (
                  <p role="alert" className="text-[12.5px] text-red">
                    {phase.failed}
                  </p>
                )}
                <input
                  value={account}
                  onChange={(e) => setAccount(e.target.value)}
                  placeholder={t("screen-screenview.account")}
                  aria-label={t("screen-screenview.account")}
                  autoComplete="username"
                  autoCapitalize="none"
                  className="h-9 rounded-control bg-field px-3 text-[13px] outline-none focus:ring-2 focus:ring-accent/40"
                />
                <input
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  placeholder={t("screen-screenview.password")}
                  aria-label={t("screen-screenview.password")}
                  type="password"
                  autoComplete="current-password"
                  className="h-9 rounded-control bg-field px-3 text-[13px] outline-none focus:ring-2 focus:ring-accent/40"
                />
                {canKeep && (
                  <label className="flex items-center gap-2 whitespace-nowrap text-[12.5px] text-ink-2">
                    <input type="checkbox" checked={remember} onChange={(e) => setRemember(e.target.checked)} className="size-3.5 shrink-0 accent-[var(--accent)]" />
                    {keepLabel(target.keep)}
                  </label>
                )}
                <button
                  type="submit"
                  disabled={!account.trim() || !password}
                  className="h-9 whitespace-nowrap rounded-control bg-accent text-[13px] font-medium text-white disabled:opacity-45"
                >
                  {t("screen-screenview.connect")}
                </button>
              </form>
            ) : (
              <div className="flex max-w-[360px] flex-col items-center gap-3 text-center">
                {(phase.kind === "checking" || phase.kind === "connecting") && <Spinner size={18} />}
                <p className="text-[13px] text-ink-2">{phaseText(phase, name)}</p>
                {phase.kind === "closed" && (
                  <button type="button" onClick={reconnect} className="h-8 whitespace-nowrap rounded-control bg-surface px-3 text-[12.5px] font-medium shadow-btn hover:bg-hover">
                    {t("screen-screenview.reconnect")}
                  </button>
                )}
              </div>
            )}
          </div>
        )}
      </main>
    </div>
  );
}

/** The status of the extension that lets this Mac's screen through, as the hub has it now: cua's computer, else ego's
 *  browser (#1507), each asked fresh and null while off (404). Throws what to tell the user when there is no screen. */
async function sharing(target: ScreenTarget): Promise<Computer> {
  const fresh = async (what: "computer" | "browser") => {
    try {
      // the hub asks the Mac for up to 45 s (extmgr/browser.go; computer.go 30 s)
      return await api<Computer>(`/machines/${encodeURIComponent(target.mid)}/${what}`, { token: target.token, timeout: pastHub(45_000) });
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return null;
      throw e;
    }
  };
  const c = await fresh("computer");
  const b = c?.enabled ? null : await fresh("browser");
  const on = c?.enabled ? c : b?.enabled ? b : null;
  if (!c && !b) throw new Error(t("screen-screenview.no_extension"));
  if (!on) throw new Error(t("screen-screenview.extension_off"));
  if (!on.screen_sharing) throw new Error(t("screen-screenview.sharing_off"));
  return on;
}

const place = (r: Rect) => ({ left: r.x, top: r.y, width: r.w, height: r.h });

function phaseLabel(p: Phase): string {
  switch (p.kind) {
    case "signin":
      return t("screen-screenview.phase_signin");
    case "closed":
    case "blocked":
      return t("screen-screenview.phase_failed");
    default:
      return t("screen-screenview.phase_connecting");
  }
}

function phaseText(p: Phase, name: string): string {
  switch (p.kind) {
    case "checking":
      return t("screen-screenview.finding", { name });
    case "connecting":
      return t("screen-screenview.connecting", { name });
    case "blocked":
    case "closed":
      return p.why;
    default:
      return "";
  }
}
