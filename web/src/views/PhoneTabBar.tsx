// The phone's tab bar (lib/phone-nav.ts "Tab bar"): the main chat, Agents, External (the chats Lark /agent and other
// extensions opened, `/history/external`) and Settings along the bottom of a narrow screen, instead of the top bar's
// "Assistant | Agents" switch. Shown only on those four places; any other page (a conversation, an Agent, To-dos…)
// keeps its own header and its way back, as with the switch. Agents carries how many agents are waiting for you, as
// the switch does. Hidden while the phone's keyboard is up (styles.css, `data-kb`), so the composer keeps its room.
import type { MouseEvent } from "react";
import { t } from "../i18n";
import { useScopedCounts } from "../lib/current-assistant";
import { href, onLinkClick, type Route } from "../lib/router";
import { useStore } from "../lib/store";
import { cx } from "../ui/atoms";
import { Icon, type IconName } from "../ui/icons";
import { WaitingBadge } from "./ShellSwitch";

export type PhoneTab = "assistant" | "agents" | "external" | "settings";

const TABS: { tab: PhoneTab; icon: IconName; to: Route }[] = [
  { tab: "assistant", icon: "home", to: { name: "persona" } },
  { tab: "agents", icon: "agent", to: { name: "agent-list" } },
  { tab: "external", icon: "at", to: { name: "history", origin: "external" } },
  { tab: "settings", icon: "gear", to: { name: "settings" } },
];

/** The tab a route is, or undefined on a page that is not one of the four (it shows no tab bar). */
export function phoneTabOf(route: Route, main?: string): PhoneTab | undefined {
  switch (route.name) {
    case "persona":
      return !route.sid || route.sid === main ? "assistant" : undefined;
    case "agent-list":
      return "agents";
    case "history":
      return route.origin === "external" ? "external" : undefined;
    case "settings":
      return "settings";
    default:
      return undefined;
  }
}

function tabLabel(tab: PhoneTab): string {
  switch (tab) {
    case "assistant":
      return t("shellswitch.assistant");
    case "agents":
      return t("agentspage.title");
    case "external":
      return t("shellswitch.external");
    case "settings":
      return t("shellswitch.settings");
  }
}

export function PhoneTabBar({ route }: { route: Route }) {
  const main = useStore((s) => s.assistant?.main);
  const tab = phoneTabOf(route, main);
  const waiting = useScopedCounts().badge;
  if (!tab) return null;
  return <PhoneTabBarView tab={tab} waiting={waiting} />;
}

/** The bar itself: `tab` is the current one, `waiting` the count on Agents (0: no badge). */
export function PhoneTabBarView({ tab, waiting, onNavigate }: { tab: PhoneTab; waiting: number; onNavigate?: (e: MouseEvent<HTMLAnchorElement>, to: Route) => void }) {
  return (
    <nav
      data-phone-tabs
      aria-label={t("shellswitch.tab_bar")}
      className="flex shrink-0 border-t border-line bg-page pb-[env(safe-area-inset-bottom)] lg:hidden"
    >
      {TABS.map((x) => {
        const on = x.tab === tab;
        const label = tabLabel(x.tab);
        const badge = x.tab === "agents" && waiting > 0 ? waiting : 0;
        return (
          <a
            key={x.tab}
            href={href(x.to)}
            onClick={(e) => (on ? e.preventDefault() : onNavigate ? onNavigate(e, x.to) : onLinkClick(e, x.to))}
            aria-current={on ? "page" : undefined}
            aria-label={badge ? t("shellswitch.waiting_label", { label, count: badge }) : label}
            className={cx(
              "relative flex min-h-12 min-w-0 flex-1 flex-col items-center justify-center gap-0.5 pt-1.5 pb-1 transition-colors duration-150",
              on ? "text-ink" : "text-ink-3 hover:text-ink-2",
            )}
          >
            <span className="relative flex">
              <Icon name={x.icon} size={22} stroke={on ? 2.1 : 1.8} />
              {badge > 0 && <WaitingBadge count={badge} className="pointer-events-none absolute -top-1.5 left-4" />}
            </span>
            <span className={cx("max-w-full truncate px-1 text-submeta", on && "font-medium")}>{label}</span>
          </a>
        );
      })}
    </nav>
  );
}
