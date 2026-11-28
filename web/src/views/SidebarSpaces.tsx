// The sidebar's top-level Spaces section (#17108, docs/web/web.md "Domain spaces"): the user's active domain spaces
// with inline conversation lists, conversation counts, one-click new conversation and quick SPACE.md view/edit.
// Stays hidden when the hub has no tracks API or when there are 0 active spaces, so empty setups stay uncluttered.
import { Suspense, lazy, useMemo, useState } from "react";
import { t } from "../i18n";
import { spaceNavKey } from "../lib/persona-nav";
import { href, type Route } from "../lib/router";
import { sidebarSpaces, spaceIcon, useTracks, type Track } from "../lib/tracks";
import { useFolded } from "../lib/use-folded";
import { Collapse, cx, sidebarFoldMotion } from "../ui/atoms";
import { Icon } from "../ui/icons";
import { conversationTitle } from "./assistant/conversations";
import { CURRENT_ROW, GroupCount, Section, type GoFn, type RegFn } from "./SidebarParts";
import { NavRow, SessionRow } from "./SidebarRows";
import { useSpaceConversations } from "./spaces/conversations";

const SpaceMdEditor = lazy(() => import("./spaces/SpaceMdEditor").then((m) => ({ default: m.SpaceMdEditor })));

const SPACE_CHATS_INITIAL = 5;
const AUTO_EXPAND_MAX_SPACES = 3;
const MORE_BUTTON = "whitespace-nowrap rounded-[7px] px-2 py-1 text-left text-label text-ink-3 hover:text-ink-2 [@media(pointer:coarse)]:min-h-11";
const ACTION_BUTTON = "inline-flex size-6 shrink-0 items-center justify-center rounded-[6px] text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink [@media(pointer:coarse)]:size-9";

export function SidebarSpaces({ route, reg, hover, activeKey, go }: { route: Route; reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn }) {
  const { tracks, supported } = useTracks();
  const spaces = useMemo(() => (tracks ? sidebarSpaces(tracks) : []), [tracks]);
  const [editingSlug, setEditingSlug] = useState<string | null>(null);
  const holdsSection = spaces.some((s) => activeKey === spaceNavKey(s.slug)) || activeKey === "track-new";
  const [folded, toggle] = useFolded("domain-spaces", false, { holds: holdsSection });

  // Keep the sidebar uncluttered when the hub has no tracks API or 0 active spaces.
  if (!supported || !tracks || spaces.length === 0) return null;

  const newSpaceRoute: Route = { name: "track-new" };
  return (
    <>
      <Section
        top
        title={t("sidebar.spaces")}
        folded={folded}
        onToggle={toggle}
        right={
          <a
            href={href(newSpaceRoute)}
            onClick={go(newSpaceRoute)}
            aria-label={t("sidebar.new_space")}
            title={t("sidebar.new_space")}
            aria-current={activeKey === "track-new" ? "page" : undefined}
            className={cx(
              "inline-flex size-6 shrink-0 items-center justify-center rounded-[6px] text-ink-3 transition-colors hover:bg-hover hover:text-ink [@media(pointer:coarse)]:size-11",
              activeKey === "track-new" && "bg-hover-2 text-ink",
            )}
          >
            <Icon name="plus" size={13} stroke={2} />
          </a>
        }
      >
        <div className="sidebar-indent" data-sidebar-spaces>
          {spaces.map((space) => (
            <SidebarSpaceItem
              key={space.slug}
              space={space}
              route={route}
              reg={reg}
              hover={hover}
              go={go}
              defaultFolded={spaces.length > AUTO_EXPAND_MAX_SPACES}
              onEditMd={() => setEditingSlug(space.slug)}
            />
          ))}
        </div>
      </Section>
      {editingSlug && (
        <Suspense fallback={null}>
          <SpaceMdEditor open slug={editingSlug} onClose={() => setEditingSlug(null)} />
        </Suspense>
      )}
    </>
  );
}

function SidebarSpaceItem({ space, route, reg, hover, go, defaultFolded, onEditMd }: {
  space: Track;
  route: Route;
  reg: RegFn;
  hover: (k: string | null) => void;
  go: GoFn;
  defaultFolded: boolean;
  onEditMd: () => void;
}) {
  const { list } = useSpaceConversations(space.slug);
  const title = space.title || space.slug;
  const inSpace = route.name === "track" && route.slug === space.slug;
  const activeSid = inSpace ? route.sid : undefined;
  const activeNew = inSpace && Boolean(route.fresh) && !route.sid;
  const [folded, toggle] = useFolded(`domain-space:${space.slug}`, defaultFolded, { holds: inSpace });
  const [more, setMore] = useState(0);

  const visibleCount = SPACE_CHATS_INITIAL * (1 + more);
  const visibleChats = list ? list.slice(0, visibleCount) : [];
  const remaining = list ? list.length - visibleChats.length : 0;
  const canCollapse = more > 0 && (list?.length ?? 0) > SPACE_CHATS_INITIAL;
  const newChatRoute: Route = { name: "track", slug: space.slug, fresh: true };

  return (
    <div data-sidebar-space={space.slug} className="flex flex-col">
      <div
        className={cx(
          "group/space relative flex w-full items-center gap-1 rounded-[7px] px-1.5 sidebar-row transition-colors hover:bg-hover",
          inSpace && !activeSid && !activeNew && CURRENT_ROW,
        )}
      >
        <button
          type="button"
          onClick={toggle}
          aria-expanded={!folded}
          aria-label={folded ? t("spaces.expand_space", { name: title }) : t("spaces.collapse_space", { name: title })}
          title={space.description || title}
          className="flex min-w-0 flex-1 items-center gap-1.5 whitespace-nowrap py-1 text-left [@media(pointer:coarse)]:min-h-11"
        >
          <Icon
            name="chevron"
            size={10}
            stroke={2.4}
            className="shrink-0 text-ink-3 transition-transform"
            style={{ ...sidebarFoldMotion(!folded), transform: folded ? "rotate(-90deg)" : "none" }}
          />
          <Icon name={spaceIcon(space)} size={13} className="shrink-0 text-ink-3" />
          <span className="min-w-0 flex-1 truncate text-meta font-medium text-ink">{title}</span>
        </button>
        <div className="flex shrink-0 items-center gap-0.5">
          {list !== null && (
            <span className="px-1">
              <GroupCount n={list.length} label={t("spaces.chat_count", { count: list.length })} />
            </span>
          )}
          <div
            className={cx(
              "flex shrink-0 items-center gap-0.5 transition-opacity",
              inSpace ? "opacity-100" : "opacity-0 group-hover/space:opacity-100 group-focus-within/space:opacity-100 [@media(hover:none)]:opacity-100",
            )}
          >
            <a
              href={href(newChatRoute)}
              onClick={go(newChatRoute)}
              aria-label={t("spaces.new_chat_in", { name: title })}
              title={t("spaces.new_chat_in", { name: title })}
              aria-current={activeNew ? "page" : undefined}
              className={cx(ACTION_BUTTON, activeNew && "bg-hover-2 text-ink")}
            >
              <Icon name="compose" size={12} stroke={1.9} />
            </a>
            <button
              type="button"
              onClick={onEditMd}
              aria-label={t("spaces.edit_md_for", { name: title })}
              title={t("spaces.edit_md_for", { name: title })}
              className={ACTION_BUTTON}
            >
              <Icon name="file" size={12} stroke={1.9} />
            </button>
          </div>
        </div>
      </div>

      <Collapse open={!folded} sidebarFold>
        <div className="sidebar-indent" inert={folded || undefined}>
          {visibleChats.map((c) => {
            const k = `track:${space.slug}/${c.session_id}`;
            return (
              <SessionRow
                key={c.session_id}
                s={c}
                k={k}
                reg={reg}
                hover={hover}
                active={activeSid === c.session_id}
                to={{ name: "track", slug: space.slug, sid: c.session_id }}
                go={go}
                label={conversationTitle(c)}
                showState={false}
                keepTrailing
              />
            );
          })}
          {list !== null && list.length === 0 && (
            <NavRow
              k={`track:${space.slug}/new`}
              reg={reg}
              hover={hover}
              active={activeNew}
              to={newChatRoute}
              go={go}
              icon="compose"
              label={t("sidebar.new_chat")}
            />
          )}
          {(remaining > 0 || canCollapse) && (
            <div className="relative z-10 flex flex-wrap">
              {remaining > 0 && (
                <button type="button" onClick={() => setMore((n) => n + 1)} className={MORE_BUTTON}>
                  {t("sidebar.show_more", { count: Math.min(SPACE_CHATS_INITIAL, remaining) })}
                </button>
              )}
              {canCollapse && (
                <button type="button" onClick={() => setMore(0)} className={MORE_BUTTON}>
                  {t("sidebar.show_less")}
                </button>
              )}
            </div>
          )}
        </div>
      </Collapse>
    </div>
  );
}
