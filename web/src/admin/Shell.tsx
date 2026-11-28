// The admin shell: navigation (left on wide screens, a strip on top on narrow ones), the restart banner, the page.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { t } from "../i18n";
import { glideStyle } from "../lib/glide";
import { useMacChromeMode } from "../lib/mac-chrome";
import { Button, cx, Dialog, EASE, toast } from "../ui/atoms";
import { BUSY_MOTION, spinStyle } from "../lib/busy-motion";
import { setTheme, useTheme, type Theme } from "../views/theme";
import { adminApi, errorText } from "./api";
import { AdminIcon, type AdminIconName } from "./icons";
import { memberNotice, restartText } from "./logic";
import { NAV, type Page } from "./nav";
import { navigate, pathOf, usePage } from "./router";
import { refreshMesh, share, useShared } from "./store";
import type { Overview } from "./types";
import { Assistant } from "./pages/Assistant";
import { Devices } from "./pages/Devices";
import { Extensions } from "./pages/Extensions";
import { Machines } from "./pages/Machines";
import { Network } from "./pages/Network";
import { OverviewPage } from "./pages/Overview";
import { Peers } from "./pages/Peers";
import { UsageSourcesPage } from "./pages/UsageSources";

export function Shell({ hubName, onLogout }: { hubName: string; onLogout: () => void }) {
  const page = usePage();
  const shared = useShared();
  const name = shared.hubName || hubName;
  // Under the desktop's macOS unified titlebar the window controls sit over the sidebar's top: it keeps the main app's
  // inset and drag strip there (styles.css, #16992). Full screen has no controls to clear.
  const chromeMode = useMacChromeMode();

  // The banner and TLS state are needed on every page: one overview fetch when the shell opens.
  useEffect(() => {
    adminApi<Overview>("/overview").then(
      (o) => share({ restart: o.restart, canRestart: o.hub.can_restart, tls: o.tls, hubName: o.hub.name, startedAt: o.hub.started_at, hubId: o.hub.hub_id }),
      () => {},
    );
  }, []);

  // Center or member: asked again on every page change (the center may have changed meanwhile, or gone offline).
  useEffect(() => {
    void refreshMesh();
  }, [page]);

  useEffect(() => {
    document.title = t("admin-shell.document_title", { page: NAV.find((n) => n.page === page)?.label ?? t("admin-shell.admin"), name: name || "Persona" });
  }, [page, name]);

  const logout = async () => {
    try {
      await adminApi("/logout", "POST");
    } catch {
      // the cookie is cleared either way; a logout_failed means the server row may outlive it until it expires
    }
    onLogout();
  };

  return (
    <div data-admin-shell data-mac-chrome={chromeMode} className="flex h-full flex-col bg-page lg:flex-row">
      <aside data-admin-sidebar className="hidden w-[232px] shrink-0 flex-col border-r border-line bg-canvas lg:flex">
        <Brand name={name} />
        <SideNav page={page} />
        <div className="mt-auto border-t border-line p-2.5">
          <ThemeSwitch />
          <button type="button" onClick={logout} className="mt-1 flex h-8 w-full items-center gap-2 whitespace-nowrap rounded-control px-2 text-[12.5px] text-ink-2 transition-colors hover:bg-hover hover:text-ink">
            <AdminIcon name="logout" size={14} />
            {t("admin-shell.sign_out")}
          </button>
        </div>
      </aside>
      <header className="shrink-0 border-b border-line bg-canvas lg:hidden">
        <div className="flex h-12 items-center gap-2 px-3">
          <Brand name={name} compact />
          <div className="ml-auto flex items-center gap-1">
            <ThemeSwitch compact />
            <button type="button" onClick={logout} aria-label={t("admin-shell.sign_out")} title={t("admin-shell.sign_out")} className="flex size-8 items-center justify-center rounded-[8px] text-ink-3 hover:bg-hover hover:text-ink">
              <AdminIcon name="logout" size={15} />
            </button>
          </div>
        </div>
        <nav aria-label={t("admin-shell.sections")} className="flex items-center gap-0.5 overflow-x-auto px-2 pb-2">
          {NAV.map((n, i) => [
            n.advanced && !NAV[i - 1]?.advanced && <span key="advanced" aria-hidden className="mx-1 h-4 w-px shrink-0 bg-line-strong" />,
            <a
              key={n.page}
              href={pathOf(n.page)}
              onClick={(e) => go(e, n.page)}
              aria-current={page === n.page ? "page" : undefined}
              className={cx(
                "flex h-8 shrink-0 items-center gap-1.5 whitespace-nowrap rounded-control px-2.5 text-[12.5px] transition-colors",
                page === n.page ? "bg-surface font-medium text-ink shadow-btn" : "text-ink-3 hover:text-ink-2",
              )}
            >
              <AdminIcon name={n.icon} size={14} />
              {n.label}
            </a>,
          ])}
        </nav>
      </header>
      <main className="min-h-0 min-w-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-[860px] px-4 py-6 sm:px-8 sm:py-8">
          <RestartBanner />
          <MemberBanner />
          <div key={page} style={{ animation: `fade-up 320ms ${EASE} both` }}>
            {page === "overview" && <OverviewPage />}
            {page === "machines" && <Machines />}
            {page === "peers" && <Peers />}
            {page === "devices" && <Devices />}
            {page === "extensions" && <Extensions />}
            {page === "assistant" && <Assistant />}
            {page === "usage" && <UsageSourcesPage />}
            {page === "network" && <Network />}
          </div>
        </div>
      </main>
    </div>
  );
}

function go(e: React.MouseEvent, page: Page) {
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
  e.preventDefault();
  navigate(page);
}

function Brand({ name, compact }: { name: string; compact?: boolean }) {
  return (
    <div className={cx("flex min-w-0 items-center gap-2.5", !compact && "px-3.5 pt-4 pb-3")}>
      <span className={cx("flex shrink-0 items-center justify-center rounded-[9px] bg-ink text-surface shadow-raised", compact ? "size-7" : "size-8")}>
        <AdminIcon name="shield" size={compact ? 14 : 16} />
      </span>
      <div className="min-w-0">
        <div className="truncate text-[13px] font-semibold text-ink">{name || "Persona hub"}</div>
        <div className="truncate text-[11px] text-ink-3">{t("admin-shell.hub_admin")}</div>
      </div>
    </div>
  );
}

// SideNav is Beautiful UI's sidebar nav: one highlight that glides to the hovered row, resting on the current page.
function SideNav({ page }: { page: Page }) {
  const box = useRef<HTMLDivElement>(null);
  const rows = useRef(new Map<Page, HTMLAnchorElement>());
  const [hover, setHover] = useState<Page | null>(null);
  const [pos, setPos] = useState<{ top: number; height: number } | null>(null);
  useLayoutEffect(() => {
    const el = rows.current.get(hover ?? page);
    const next = el ? { top: el.offsetTop, height: el.offsetHeight } : null;
    setPos((p) => (p?.top === next?.top && p?.height === next?.height ? p : next));
  });
  return (
    <nav ref={box} aria-label={t("admin-shell.sections")} onMouseLeave={() => setHover(null)} className="relative flex flex-col gap-px px-2.5">
      <span
        aria-hidden
        className="pointer-events-none absolute inset-x-2.5 rounded-[7px] bg-hover"
        style={glideStyle(pos, Boolean(pos), EASE)}
      />
      {NAV.map((n, i) => [
        n.advanced && !NAV[i - 1]?.advanced && (
          <div key="advanced" className="px-2 pt-4 pb-1 text-[11px] font-medium text-ink-3">
            {t("admin-shell.advanced")}
          </div>
        ),
        <a
          key={n.page}
          ref={(el) => {
            if (el) rows.current.set(n.page, el);
          }}
          href={pathOf(n.page)}
          onClick={(e) => go(e, n.page)}
          onMouseEnter={() => setHover(n.page)}
          aria-current={page === n.page ? "page" : undefined}
          className={cx("relative z-10 flex h-8 items-center gap-2.5 whitespace-nowrap rounded-[7px] px-2 text-[13px]", page === n.page ? "font-medium text-ink" : "text-ink-2")}
        >
          <AdminIcon name={n.icon} size={15} className={page === n.page ? "text-ink" : "text-ink-3"} />
          {n.label}
        </a>,
      ])}
    </nav>
  );
}

const THEMES: { t: Theme; icon: AdminIconName; label: string }[] = [
  { t: "system", icon: "monitor", label: t("admin-shell.theme_system") },
  { t: "light", icon: "sun", label: t("admin-shell.theme_light") },
  { t: "dark", icon: "moon", label: t("admin-shell.theme_dark") },
];

function ThemeSwitch({ compact }: { compact?: boolean }) {
  const theme = useTheme();
  return (
    <div role="radiogroup" aria-label={t("admin-shell.appearance")} className={cx("flex items-center gap-0.5 rounded-control bg-inset p-0.5 shadow-hairline", !compact && "w-full")}>
      {THEMES.map((o) => (
        <button
          key={o.t}
          type="button"
          role="radio"
          aria-checked={theme === o.t}
          aria-label={o.label}
          title={o.label}
          onClick={() => setTheme(o.t)}
          className={cx(
            "flex h-7 items-center justify-center rounded-[7px] transition-[background-color,color] duration-150",
            compact ? "w-7" : "flex-1",
            theme === o.t ? "bg-surface text-ink shadow-btn" : "text-ink-3 hover:text-ink-2",
          )}
        >
          <AdminIcon name={o.icon} size={13} />
        </button>
      ))}
    </div>
  );
}

function RestartBanner() {
  const { restart, canRestart, startedAt } = useShared();
  const [asking, setAsking] = useState(false);
  const [phase, setPhase] = useState<"idle" | "restarting" | "slow">("idle");
  if (!restart.required && phase === "idle") return null;

  const run = async () => {
    setAsking(false);
    setPhase("restarting");
    try {
      await adminApi("/restart", "POST");
    } catch (e) {
      setPhase("idle");
      toast(errorText(e), "error");
      return;
    }
    // Back when the overview answers with a newer start time; give up waiting after 90 seconds.
    const until = Date.now() + 90_000;
    let down = false;
    while (Date.now() < until) {
      await new Promise((r) => setTimeout(r, 1500));
      try {
        const o = await adminApi<Overview>("/overview");
        if (startedAt ? o.hub.started_at !== startedAt : down) {
          share({ restart: o.restart, canRestart: o.hub.can_restart, tls: o.tls, hubName: o.hub.name, startedAt: o.hub.started_at });
          setPhase("idle");
          toast(t("admin-shell.restarted"), "ok");
          return;
        }
      } catch {
        down = true; // still on its way back
      }
    }
    setPhase("slow");
  };

  if (phase === "restarting")
    return (
      <Banner tone="accent" title={t("admin-shell.restarting")} detail={t("admin-shell.restarting_detail")} />
    );
  if (phase === "slow")
    return <Banner tone="red" title={t("admin-shell.slow")} detail={t("admin-shell.slow_detail")} action={<Button onClick={() => setPhase("idle")}>{t("admin-shell.got_it")}</Button>} />;

  return (
    <>
      <Banner
        tone="orange"
        title={restartText(restart)}
        detail={canRestart ? restart.reasons.join(t("admin-shell.reason_separator")) : t("admin-shell.restart_manual", { reasons: restart.reasons.join(t("admin-shell.reason_separator")) })}
        action={canRestart && (
          <Button tone="primary" onClick={() => setAsking(true)}>
            <AdminIcon name="power" size={13} stroke={2.2} />
            {t("admin-shell.restart_hub")}
          </Button>
        )}
      />
      <Dialog
        open={asking}
        onClose={() => setAsking(false)}
        title={t("admin-shell.restart_confirm_title")}
        footer={
          <>
            <Button tone="quiet" onClick={() => setAsking(false)}>
              {t("admin-parts.cancel")}
            </Button>
            <Button tone="primary" onClick={run}>
              {t("admin-shell.restart")}
            </Button>
          </>
        }
      >
        <p className="text-[13px] leading-relaxed text-ink-2">
          {t("admin-shell.restart_confirm_body")}
        </p>
      </Dialog>
    </>
  );
}

// MemberBanner: on a member hub every page says the group is the center's to run (docs/archive/operations/plan-hub-center.md 3).
function MemberBanner() {
  const { mesh } = useShared();
  const page = usePage();
  const n = memberNotice(mesh);
  if (!n) return null;
  return (
    <Banner
      tone={n.offline ? "orange" : "accent"}
      icon="mesh"
      title={n.title}
      detail={n.detail}
      action={n.offline && page !== "peers" && <Button onClick={() => navigate("peers")}>{t("admin-shell.change_primary")}</Button>}
    />
  );
}

function Banner({ tone, title, detail, action, icon }: { tone: "orange" | "accent" | "red"; title: string; detail?: string; action?: React.ReactNode; icon?: AdminIconName }) {
  const bg = { orange: "bg-orange-tint", accent: "bg-accent-tint", red: "bg-red-tint" }[tone];
  const ink = { orange: "text-orange", accent: "text-accent-ink", red: "text-red" }[tone];
  const spin = !icon && tone === "accent"; // the restart in progress
  return (
    <div role="status" className={cx("mb-6 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-card px-3.5 py-3", bg)} style={{ animation: `fade-up 300ms ${EASE} both` }}>
      <AdminIcon name={icon ?? (spin ? "retry" : "alert")} size={16} className={cx("shrink-0 self-start", ink, spin && BUSY_MOTION)} style={spin ? spinStyle(1200) : undefined} />
      <div className="min-w-[200px] flex-1">
        <div className={cx("text-[13px] font-medium", ink)}>{title}</div>
        {detail && <div className="mt-0.5 text-[12px] leading-relaxed text-ink-2">{detail}</div>}
      </div>
      {action && <div className="shrink-0">{action}</div>}
    </div>
  );
}
