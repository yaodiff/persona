// Once the hub answers with a roster, even with one assistant, the sidebar's head names the current one and opens
// the roster, followed by appearance, the other hubs, Settings and Disconnect. The remembered identity keeps that
// assistant head while the roster loads or a temporary read fails. A hub without a roster keeps the earlier product
// row (AppMark, Persona and the hub's connection status) and its original menu.
// The current pick is kept per hub (lib/manager-choice.ts), including while its roster loads after a reload.
import { memo, useId, useLayoutEffect, useRef, useState } from "react";
import { t } from "../i18n";
import { useAssistantIdentity } from "../lib/assistant-identity";
import { BREATHING, connStatus, phaseTone, type ConnPhase } from "../lib/connection";
import { chooseManager, useShownManager } from "../lib/manager-choice";
import { useManagerBadges } from "../lib/manager-attention";
import { loadRoster, managerError, managerOf, managerOrder, multiManager, renewCode, statusLine, switcherLine, switcherManagers, useRoster, type InstallCode, type Manager, type Tone } from "../lib/managers";
import { refreshReadiness } from "../lib/readiness";
import { navigate, type Route } from "../lib/router";
import { routeAfterSwitch } from "../lib/persona-nav";
import { sameValue } from "../lib/stable-lines";
import { AppMark } from "../ui/AppMark";
import { AssistantAvatar } from "../ui/AssistantAvatar";
import { PersonaAvatar } from "./assistant/Persona";
import { cx, Menu, Spinner, toast, useMenu, useNow, type MenuItem } from "../ui/atoms";
import { Icon, type IconName } from "../ui/icons";
import { ASSISTANT_MENU_WIDTH } from "../ui/menu-model";
import { useDisconnect } from "./DisconnectBrowser";
import { hubMenuItems, useHubs } from "./hub-menu";
import { lazyDialog } from "./LazyOverlay";
import type { ShownCode } from "./ManagerInstall";
import { setTheme, useTheme, type Theme } from "./theme";

// New assistant and the install code open only on a click: their chunk (shared with Settings › Assistants) loads on the
// first open, not with the first screen (src/startup-preload.ts FIRST_SCREEN_BUDGET).
type CodeProps = { open: boolean; shown: ShownCode | null; onClose: () => void; onCode: (c: InstallCode, id: string) => void };
const NewManager = lazyDialog(() => import("./ManagerInstall").then((m) => m.NewManager));
const CodeDialog = lazyDialog<CodeProps>(() => import("./ManagerInstall").then(({ CodeDialog: Code }) => ({ open: _open, ...rest }: CodeProps) => <Code {...rest} />));

// The install codes this page was given, by manager: a row still installing shows its code again while it lasts,
// rather than asking for a new one (which ends the old).
const codes = new Map<string, InstallCode>();

/** Makes `m` the current assistant and shows the page that is its own: a page about one thing of the assistant left
 *  behind goes back to that thing's list (lib/persona-nav.ts routeAfterSwitch). */
export function switchAssistant(m: Manager, route: Route) {
  chooseManager(m.id);
  const to = routeAfterSwitch(route);
  if (to) navigate(to);
  void refreshReadiness({ force: true });
  toast(t("sidebar.switched_to", { name: m.name }), "ok");
}

type SwitcherProps = { phase: ConnPhase; online: number; total: number; route: Route };
const NO_BADGES: Record<string, number> = {};

// The status dot's colours, matching AssistantAvatar's (its disc is replaced by the look's face for the default
// assistant, but the corner marks read the same).
const FACE_STATUS: Record<Tone, string> = { green: "bg-green", grey: "bg-line-strong", accent: "bg-orange" };

/** The default assistant's head: the chosen look's face with the same presence and waiting marks as a monogram. */
const DefaultAssistantFace = memo(function DefaultAssistantFace({ tone, waiting }: { tone?: Tone; waiting: boolean }) {
  const mark = 9;
  return (
    <span aria-hidden className="relative inline-flex shrink-0" style={{ width: 32, height: 32 }}>
      <PersonaAvatar size={32} className="shadow-hairline" />
      {tone && (
        <span
          data-avatar-status={tone}
          className={cx("absolute -right-px -bottom-px rounded-full ring-2 ring-canvas", FACE_STATUS[tone])}
          style={{ width: mark, height: mark }}
        />
      )}
      {waiting && (
        <span
          data-avatar-waiting
          className="absolute -top-px -right-px rounded-full bg-attention ring-2 ring-canvas"
          style={{ width: mark, height: mark }}
        />
      )}
    </span>
  );
});

// Waiting counts keep their separate gate: only several installed assistants subscribe to sessions and cards. One
// assistant still shows identity and the roster without subscribing to those counts, and SwitcherRow stays mounted
// across roster arrival so the avatar and menu never flash or reset.
export const AssistantSwitcher = memo(function AssistantSwitcher(props: SwitcherProps) {
  const several = multiManager(useRoster());
  const [badges, setBadges] = useState<Record<string, number>>(NO_BADGES);
  return (
    <>
      {several && <ManagerBadgesCollector onChange={setBadges} />}
      <SwitcherRow {...props} badges={several ? badges : NO_BADGES} />
    </>
  );
});

export function ManagerBadgesCollector({ onChange }: { onChange: (badges: Record<string, number>) => void }) {
  const badges = useManagerBadges();
  const published = useRef<Record<string, number> | undefined>(undefined);
  useLayoutEffect(() => {
    if (published.current && sameValue(published.current, badges)) return;
    published.current = badges;
    onChange(badges);
  }, [badges, onChange]);
  return null;
}

/** The roster's rows (each assistant, then New assistant and Manage assistants…) and the dialogs they open: the one list
 *  the sidebar's head and Settings' Switch both show. `open`: whether the list is open (its lines count minutes then). */
function useAssistantList(route: Route, badges: Record<string, number>, open: boolean) {
  const roster = useRoster();
  const current = useShownManager();
  const [creating, setCreating] = useState(false);
  const [shown, setShown] = useState<ShownCode | null>(null);
  const [asking, setAsking] = useState(false);
  // the lines count minutes (an install code running out, "last synced"): once a minute while the list is open
  const now = useNow(30_000, open);

  // a manager still installing: its install commands, with the code this page has while it lasts, else a new one
  const showCode = async (m: Manager) => {
    const kept = codes.get(m.id);
    if (kept && Date.parse(kept.expires_at) > Date.now()) return setShown({ id: m.id, name: m.name, code: kept, fresh: true });
    setAsking(true);
    try {
      const r = await renewCode(m.id, m.name, true);
      codes.set(r.id, r.code);
      setShown({ id: r.id, name: m.name, code: r.code, fresh: true });
      void loadRoster();
    } catch (e) {
      toast(managerError(e), "error");
    } finally {
      setAsking(false);
    }
  };

  const installed = roster ? managerOrder(roster) : [];
  const installing = roster ? roster.managers.filter((m) => m.installing) : [];
  // the Default tag counts a hidden built-in too (it still runs); only its row is left out (#9055)
  const several = installed.length > 1;
  const listed = roster ? switcherManagers(roster) : [];
  const row = (m: Manager): MenuItem => ({
    key: `assistant:${m.id}`,
    label: m.name,
    tag: several && m.id === roster?.default ? t("settings-managers.default") : undefined,
    detail: switcherLine(m, now),
    lead: <AssistantAvatar id={m.id} name={m.name} isDefault={m.id === roster?.default} size={28} status={statusLine(m, now).tone} ring="ring-surface" />,
    // an offline one: the whole row faded (avatar, name and line), still chosen like the others
    dim: !m.online && !m.installing,
    ...(m.installing ? {} : { radio: true, checked: m.id === current, badge: m.id === current ? 0 : badges[m.id] ?? 0, badgeLabel: t("sidebar.switcher_count", { count: badges[m.id] ?? 0 }) }),
    onSelect: () => {
      if (m.installing) void showCode(m);
      else if (m.id !== current) switchAssistant(m, route);
    },
  });
  const items: MenuItem[] = roster
    ? [
        ...[...listed, ...installing].map(row),
        { key: "new-assistant", label: t("sidebar.new_assistant"), icon: "plus" as const, divider: true, onSelect: () => setCreating(true) },
        { key: "manage-assistants", label: t("sidebar.manage_assistants"), icon: "gear" as const, onSelect: () => navigate("/settings?tab=managers") },
      ]
    : [];
  const dialogs = (
    <>
      <NewManager
        open={creating}
        onClose={() => setCreating(false)}
        onMade={(m, code) => {
          setCreating(false);
          codes.set(m.id, code);
          setShown({ id: m.id, name: m.name, code, fresh: true });
          void loadRoster();
        }}
      />
      <CodeDialog
        open={Boolean(shown)}
        shown={shown}
        onClose={() => setShown(null)}
        onCode={(code, id) => {
          codes.set(id, code);
          setShown((s) => (s ? { ...s, code, id } : s));
          void loadRoster();
        }}
      />
    </>
  );
  return { items, dialogs, asking, now };
}

const SwitcherRow = memo(function SwitcherRow({ phase, online, total, route, badges }: SwitcherProps & { badges: Record<string, number> }) {
  const menu = useMenu();
  const theme = useTheme();
  const roster = useRoster();
  const current = useShownManager();
  const otherHubs = hubMenuItems(useHubs());
  const disconnect = useDisconnect();
  const list = useAssistantList(route, badges, Boolean(menu.anchor));
  const { now, asking } = list;
  const statusId = useId();
  const currentRecord = managerOf(roster, current);
  const known = currentRecord?.installing ? null : currentRecord;
  const me = useAssistantIdentity();
  const hasAssistant = me !== null;
  const linked = phase === "online";
  const othersWaiting = Object.entries(badges).some(([id, n]) => id !== current && n > 0);

  const themes: { k: Theme; label: string; icon: IconName }[] = [
    { k: "system", label: t("sidebar.theme_system"), icon: "monitor" },
    { k: "light", label: t("sidebar.theme_light"), icon: "sun" },
    { k: "dark", label: t("sidebar.theme_dark"), icon: "moon" },
  ];

  const items: MenuItem[] = [
    ...(hasAssistant ? list.items : []),
    // the three looks as rows of their own under a small title, as the row's menu had them: a submenu would open over
    // the rows under it on a narrow screen (a phone, a zoomed page) and hide Settings and Disconnect
    ...(hasAssistant ? [{ key: "appearance", label: t("sidebar.appearance"), heading: true, divider: true, onSelect: () => {} }] : []),
    ...themes.map((x) => ({ key: x.k, label: x.label, icon: x.icon, checked: theme === x.k, onSelect: () => setTheme(x.k) })),
    // the other hubs in this browser's list (#1014); none, and the menu is as before
    ...otherHubs,
    { key: "settings", label: t("sidebar.nav_settings"), icon: "gear", divider: otherHubs.length > 0, onSelect: () => navigate({ name: "settings" }) },
    { key: "logout", label: t("sidebar.disconnect"), icon: "logout", danger: true, onSelect: disconnect.start },
  ];

  const name = me?.name ?? "Persona";
  const tone = known ? statusLine(known, now).tone : me?.tone;
  const assistantLine = known ? switcherLine(known, now) : me?.line;
  // The default assistant is the Persona of the main conversation: show its chosen look's face (so changing the look
  // changes this face too), not the first-letter disc every other assistant keeps (#6401). While the roster loads, keep
  // the remembered default flag so the head does not flash a green first-letter circle before the roster arrives.
  const isDefaultAssistant = !!me && (roster ? me.id === roster.default : Boolean(me.isDefault));
  return (
    <>
      <button
        type="button"
        onClick={menu.open}
        aria-haspopup="menu"
        aria-expanded={Boolean(menu.anchor)}
        aria-label={me ? t(othersWaiting ? "sidebar.switcher_label_waiting" : "sidebar.switcher_label", { name }) : undefined}
        aria-describedby={me ? statusId : undefined}
        data-assistant-switcher={me?.id ?? ""}
        className={cx("flex w-full items-center gap-2.5 rounded-control p-1.5 text-left transition-[background-color,transform] duration-100 hover:bg-hover active:scale-[0.98]", menu.anchor && "bg-hover")}
      >
        {me ? (
          isDefaultAssistant ? (
            <DefaultAssistantFace tone={linked ? tone : undefined} waiting={othersWaiting} />
          ) : (
            <AssistantAvatar id={me.id} name={me.name} isDefault={false} size={32} status={linked ? tone : undefined} waiting={othersWaiting} />
          )
        ) : (
          <AppMark size={32} radius={8} className="shadow-hairline" />
        )}
        <span className="min-w-0 flex-1">
          <span className="block truncate text-[13px] leading-tight font-medium text-ink">{name}</span>
          <span id={statusId} className="flex items-center gap-1.5 truncate text-[11px] leading-tight text-ink-3">
            {/* the hub out of reach matters more than the assistant's own state: its line and dot take the place */}
            {assistantLine && linked ? (
              <span className="truncate">{assistantLine}</span>
            ) : (
              <>
                <span aria-hidden data-conn-dot className={cx("size-1.5 shrink-0 rounded-full", phaseTone(phase).color)} style={phaseTone(phase).breathing ? BREATHING : undefined} />
                <span className="truncate">{connStatus(phase, online, total)}</span>
              </>
            )}
          </span>
        </span>
        {/* the menu is gone while the hub revokes this browser's token or makes an install code: the row says it is busy */}
        {disconnect.busy || asking ? <Spinner size={12} /> : <Icon name="chevron" size={12} stroke={2} className={cx("shrink-0 text-ink-3 transition-transform duration-200", menu.anchor && "rotate-180")} />}
      </button>
      {menu.anchor && <Menu anchor={menu.anchor} onClose={menu.close} width={hasAssistant ? ASSISTANT_MENU_WIDTH.roster : ASSISTANT_MENU_WIDTH.product} header={hasAssistant ? t("sidebar.assistants") : t("sidebar.appearance")} items={items} />}
      {disconnect.dialog}
      {list.dialogs}
    </>
  );
});

/** Settings' Switch beside "<name>'s settings": the sidebar's list of assistants (the same rows: switch, New assistant,
 *  Manage assistants…) under a text button. Shown with one assistant too; a hub without a roster has none to show. */
export function AssistantSwitchButton({ route }: { route: Route }) {
  const several = multiManager(useRoster());
  const [badges, setBadges] = useState<Record<string, number>>(NO_BADGES);
  return (
    <>
      {several && <ManagerBadgesCollector onChange={setBadges} />}
      <SwitchButton route={route} badges={several ? badges : NO_BADGES} />
    </>
  );
}

function SwitchButton({ route, badges }: { route: Route; badges: Record<string, number> }) {
  const menu = useMenu();
  const roster = useRoster();
  const list = useAssistantList(route, badges, Boolean(menu.anchor));
  if (!roster) return null;
  return (
    <>
      <button
        type="button"
        onClick={menu.open}
        aria-haspopup="menu"
        aria-expanded={Boolean(menu.anchor)}
        aria-label={t("settings-nav.switch_label")}
        data-settings-switch
        className="touch-hit inline-flex shrink-0 items-center gap-1 rounded-control px-1.5 py-0.5 text-[12px] font-medium whitespace-nowrap text-accent-ink transition-colors hover:bg-hover"
      >
        {list.asking ? <Spinner size={12} /> : null}
        {t("settings-nav.switch")}
      </button>
      {menu.anchor && <Menu anchor={menu.anchor} onClose={menu.close} width={264} header={t("sidebar.assistants")} items={list.items} />}
      {list.dialogs}
    </>
  );
}
