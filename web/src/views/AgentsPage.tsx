// The phone's "Agents" page (`/agents`, #1996): the sidebar's Agents block filling the page, next to the assistant in the top bar's
// switch. Its top bar has ChatView's side slots (sidebar on the left, "New agent" on the right), so switching pages leaves
// the buttons in place. A wide screen opens it too, as a page titled Agents (no switch there). Search chats (⌘K) sits
// beside New agent: the list has no search field of its own, and the sidebar's search box is in the drawer here (#6589).
import type { Route } from "../lib/router";
import { openPalette } from "../lib/palette";
import { IconButton } from "../ui/atoms";
import { ShellHeaderSlots } from "./ShellSwitch";
import { AgentsList } from "./Sidebar";
import { t } from "../i18n";
import type { WorkspaceTreeActions } from "./SidebarTree";

export function AgentsPage({ route, onToggleSidebar, onNewAgent, workspaceActions }: { route: Route; onToggleSidebar: () => void; onNewAgent: () => void; workspaceActions?: WorkspaceTreeActions }) {
  return (
    <div className="flex h-full min-w-0 flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line bg-page/85 px-3 backdrop-blur-md sm:px-4">
        <h1 className="hidden min-w-0 flex-1 truncate text-[14px] font-semibold text-ink lg:block">{t("agentspage.title")}</h1>
        <ShellHeaderSlots page="agents" onToggleSidebar={onToggleSidebar} endWide end={<><IconButton icon="search" label={t("sidebar.search_sessions")} onClick={() => openPalette("jump")} /><IconButton icon="plus" label={t("agentspage.new_agent")} onClick={onNewAgent} /></>} />
      </header>
      <div className="min-h-0 flex-1">
        <AgentsList route={route} onNewAgent={onNewAgent} workspaceActions={workspaceActions} />
      </div>
    </div>
  );
}
