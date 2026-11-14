// Shared sizing and recipes for measured, variable-height session rows.
// pickMenuMin: the least a value pill's menu is wide (the menu's own default width); wider pills set their menu's width.
// The list by group: groupsOpen sections start open, each showing groupPreview rows before "Load more"; groupsRefresh
// is the least time between two reads of the groups themselves, however many hints come.
// The complete list's long groups (views/WindowedRows.tsx): windowHead rows always drawn, about a phone's screen of
// them, then parts of windowPart rows drawn near the view.
export const SESSION_LIST_METRICS = { estimate: 44, overscan: 6, searchDelay: 250, groupOptions: 50, avatar: 22, icon: 12, pickMenuMin: 208,
  groupsOpen: 3, groupPreview: 6, groupsRefresh: 1500, windowHead: 30, windowPart: 20 } as const;
export const SESSION_LIST_STYLE = {
  foldButton: "touch-hit relative z-10 flex size-6 shrink-0 items-center justify-center rounded-control text-ink-3 hover:bg-hover hover:text-ink-2 [@media(pointer:coarse)]:size-11",
  heading: "mt-3 flex items-center gap-1 px-2",
  headingText: "min-w-0 flex-1 text-[10.5px] font-medium tracking-[0.06em] text-ink-3 uppercase",
  personalPage: "mx-auto max-w-[760px] px-5 py-6 sm:px-8",
  personalRow: "flex items-center gap-2 px-4 py-2 hover:bg-hover",
  personalTitle: "min-w-0 flex-1 truncate text-[13px] text-ink",
  personalBadge: "rounded-full bg-accent px-1.5 text-[10.5px] font-semibold text-white tabular-nums",
  pinButton: "flex size-6 shrink-0 items-center justify-center rounded-control text-ink-3 hover:bg-hover",
  // a session drawn under the one that started it (#5935): a level in, with a line down its side, as the sidebar tree's
  // spawned agents (SidebarTree's TreeAgentRows); consecutive ones join into one line
  nestedRow: "ml-2 border-l border-line pl-3",
} as const;
