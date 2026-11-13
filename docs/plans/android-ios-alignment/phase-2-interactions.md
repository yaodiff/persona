# Phase 2: interactions (#4210)

Part of the [Android alignment plan](README.md) (#4194). Status: plan, 2026-11-11.

## Requirements

### Goal

The everyday gestures and movements on Android work as they do on the iPhone, wherever the platform allows. What isn't copied is written down with its reason and an alternative.

### Changes

**I1. The Inbox swipes as on iOS (decision 5).** On iOS one swipe from right to left completes an item, and in the done list the same swipe moves it back; there is no swipe the other way, and none while picking items. Android swipes right for Done and left to hand the item to Persona, even for done items. Requirement: Android swipes as iOS does, in the Inbox rows and the conversation rows, and the release notes say so (principle 1).

**I2. Dragging a chat dismisses the keyboard.** On iOS, starting to drag the conversation hides the keyboard. On Android only a tap on empty space does. Requirement: as on iOS, in the Agent chat, the assistant chat and Lark chats, which share one list.

**I3. The send button moves as on iOS.** On iOS the send button grows out of the right edge through a slight blur and fades in when there is something to send, stop and voice shrink away, and a press shrinks the button. On Android the buttons swap instantly. Requirement: the same movement, and none when animations are off.

**I4. Expanded content unfolds.** On iOS, opening a work step or a tool call unfolds the content downward while the tapped header stays where it was. On Android the new rows push the header up. Requirement: as on iOS.

**I5. Text is selected in place.** On iOS, Select Text (or a second long press) selects the words of the message where it is. On Android, Select opens a full-screen reader showing the raw Markdown. Requirement: select in place.

Haptics, hit targets and the feedback layer are in [phase 1](phase-1-tokens-and-guards.md).

### Waiting for the owner

**Q4. Where pending requests sit.** iOS stacks the request cards above the composer, which stays usable; past half the page they collapse into "N pending", permissions first, and each card folds to a one-line preview when its header is tapped. Android draws one request at a time in place of the composer, with "n / total" paging, and hides the composer, the queue strip and the source note until the request is answered or folded. `DESIGN_SYSTEM.md` section 4.8 describes paging ("1 of 3, one open at a time"), which matches Android, not the iOS code. Proposal: follow the iOS code, and correct section 4.8 in the same PR. This is a large change (section 1.4). Decided (2026-11-12): follow the iOS code (T6).

**Q6. Text size.** iOS offers Small, Standard and Large, one step below or above the system size; sizes that go with the text (icons, buttons, avatars) scale with it, up to 1.6 times. Android only follows the system font scale, and its icons and controls don't scale. Proposal: the same setting on Android, in Settings › Interface style.

### Not copied

| iOS | Android keeps | Why |
| --- | --- | --- |
| Swiping between Assistant and Agents, dragging out the sidebar | Tapping the switcher and the avatar | Decision 3; the screen edges are Android's back gesture |
| The right-edge scrubber | Nothing for now; another entry point if needed | The right edge is Android's back gesture, and the system exempts at most 200dp per edge from it |
| The recent-photos grid in the attachment panel | The system photo picker; add video (today it takes up to four images) | The grid needs broad media access, against Android's conventions and the store's policy |
| The blurred lift-up message menu | The action sheet; a lift over a dimmed background later | Compose has no context menu with a preview |
| Hold-to-talk dictation with slide to cancel and lock | The system voice input | Recognizers differ between phone makers, and the app needs no microphone permission this way |

The swipe that closes an Agents row stays on Android: the iPhone has none (only the Mac does), but nothing on Android competes with it. The code comment that calls it an iOS behavior is corrected.

### Acceptance criteria

- Each changed interaction matches iOS in side-by-side recordings or screenshots, light and dark (principle 4).
- The Inbox swipe change is in the release notes (principle 1).
- Everything not copied is listed in `DESIGN_SYSTEM.md` with its reason (principle 4; the section comes with phase 4).
- The owner looks at the screenshots of any change to how pending requests look or work before merge (`DESIGN_SYSTEM.md` section 1.4).

### Out of scope

- Swiping between pages and dragging the sidebar (decision 3).
- Brief and Board beyond keeping them working (decision 8).

## Technical design

### T1. The Inbox swipe

**Today.** iOS: one trailing full swipe in `apple/Persona/Views/Inbox/InboxView.swift`, Done in the positive color or Move Back in the accent color with an undo icon, and no swipe while picking. Its context menu has Done or Move Back, the mutes and the rules; handing the item to the assistant lives on the item's page. A single item gives a tap haptic.

Android: `android/feature/inbox/src/main/kotlin/persona/feature/inbox/InboxItemRow.kt` and `android/feature/inbox/src/main/kotlin/persona/feature/inbox/InboxConversationItemRow.kt` put Done or Move Back on the start side and the hand-off on the end side, and both long-press sheets offer the hand-off. The row component, `SwipeActions.kt` in the design system, wraps Material's swipe-to-dismiss: full swipes only, no accessibility actions, and color only through a status.

**Change.**

1. In both row files: no start action; the end action is Done, or Move Back with the undo-arrow icon when the item is done, and none while busy or picking. Rows in the done section get Move Back on the same swipe.
2. The component gets a tone (accent or positive) and accessibility actions, so TalkBack users reach the same actions without swiping.
3. The haptic follows iOS: a tap for a single item (today Android plays success).
4. The hand-off stays in the long-press sheets for now; #3901 replaces it with forwarding.

Risk: with gesture navigation, a swipe that starts at the very right edge is the system's back. Starting inside the row works, as in other Android apps with row swipes.

### T2. Dismissing the keyboard on drag

**Today.** iOS hides the keyboard when a real drag starts on the conversation (in `apple/Persona/Views/Chat/ChatView.swift`; the flipped list ignores SwiftUI's own setting). Android's chats share a reversed `LazyColumn` in `android/core/chatlist/src/main/kotlin/persona/core/chatlist/ChatList.kt`; only a tap on empty space hides the keyboard (`KeyboardDismiss.kt`), and a drag is consumed before it gets there.

**Change.** `ChatList.kt` already collects drag interactions; on a drag start, clear the focus and hide the keyboard. An interactive keyboard that follows the finger is not worth its custom code. Like iOS, a drag also takes the focus from a field in a request card.

Done (11-13): in every style (a drag is behaviour), `ChatListEffects` clears the focus and hides the keyboard when the list reports a `DragInteraction.Start`, which only a finger past the touch slop starts: the two calls the page's tap on blank space already makes (`dismissKeyboardOnBlankTap`). Like iOS's `Keyboard.dismiss()` it takes the focus from any field, so the field of a request card goes too, since the pending stack's cards sit outside the list. The Agent chat and Persona's conversation (`ChatScreen`) and the Lark chats (`LarkScreen`) all draw `ChatList` under `ChatListEffects`, so the one change covers the three. A tap on blank space still puts the keyboard away, once; the field, a link and a long press leave it; a scroll the page starts (following the newest message, a jump, a search hit) is not a drag and leaves it. The keyboard does not follow the finger, as planned. Tests `ChatKeyboardDismissTest` (the list under a focused composer, in every style: a drag each way, a card's field, a scroll the page starts, a tap, a long press), `ChatDragKeyboardTest` (the real Agent chat and Persona's conversation, with a request card too, and a message arriving while someone types) and `LarkDragKeyboardTest`; with the change taken out, the eight drag tests fail and the rest pass. No screenshots: nothing changes in how anything looks. Left: a soft keyboard on a device or emulator (Robolectric has none and is not in touch mode, so the tests check what the page asks of the focus manager and the keyboard controller); the Lark thread sheet (`LarkThreadSheet`), whose reversed list is its own and not the shared `ChatList`, keeps its keyboard when dragged.

### T3. The send button

**Today.** iOS (`apple/Persona/DesignSystem/ShellComposerBar.swift`, `apple/Persona/DesignSystem/ComposerButtons.swift`, curves in `apple/Shared/Design/Theme.swift`): send grows from a scale of 0.001 at the right edge while a 4 pt blur clears, fading in over 0.2 s; stop and voice shrink to 0.2 and fade; the swap follows the curve (0.38, 0.7, 0.125, 1) over 0.4 s; a press shrinks send to 0.75. With Reduce Motion it is a plain state change. Android (`android/feature/chat/src/main/kotlin/persona/feature/chat/ChatComposer.kt`) swaps send and stop with a plain `when` and has no press scale.

**Change.** Wrap the swap in `AnimatedContent`: scale in from 0.001 with the origin at the right edge, over 400 ms on that curve, with a 200 ms fade, against a scale out to 0.2 with a fade. Blur with `Modifier.blur` on Android 12 and later (nothing on Android 11). Shrink on press through the pressed state. Snap when the system's animations are off. The durations and the curve come from the motion tokens (phase 1, T3).

Risks: the leaving button stays clickable during the animation, so disable it as it leaves; the trailing slot moves between rows when the composer expands, which restarts the animation.

### T4. Unfolding in place

**Today.** iOS: `.unfolding` in `apple/Persona/Views/Chat/ChatPin.swift` reveals new content with a mask that grows downward plus a fade (off with Reduce Motion), over 0.28 s ease-out, and conceals over 0.2 s ease-in. `toggleInPlace` applies the height change at once, without animation, and `ChatPin.hold` scrolls the list on every layout pass for up to 1.5 s, so the tapped header stays still. It is used in 12 files. Android: work steps are separate items of the reversed list in `ChatScreen.kt`, so steps inserted under the header push it up; thoughts and tool calls grow inside their rows.

**Change.**

1. A hold in `core/chatlist`: before the toggle, read the header's offset; then keep it with `requestScrollToItem(index, offset)` until the layout settles, 1.5 s pass or the user drags. Pause the follow-the-end scrolling meanwhile.
2. Reveal new items with a clip and a fade drawn in `drawWithContent`, without animating heights. Closing conceals first and removes after.

Risks: offset arithmetic in a reversed list; a header that is off screen; steps streaming in while open; the saved reading position during a hold.

### T5. Selecting in place

**Today.** iOS (`apple/Persona/Views/Chat/LiftSelection.swift`) redraws the lifted message in a text view, because its Markdown renderer can't select. Android's Select opens `FullTextReader.kt`, full screen, with the raw Markdown.

**Change.** The Markdown renderer Android uses draws with Compose `Text`, so one message can be wrapped in a `SelectionContainer`. Select makes only that message selectable and turns off its long press, its swipe to quote and its inline-code copy until the selection ends. The Lark views already select in place with their own menu items (`android/feature/inbox/src/main/kotlin/persona/feature/inbox/LarkTextPicks.kt`), and a Quote item can reuse that.

Risks: no selection across messages; a selection may be lost when the message scrolls away; Compose may have no public way to start with the word under the finger selected (to check); inline math images and scrolling code blocks.

### T6. Pending requests above the composer (if Q4 is yes)

**Today.** iOS: `apple/Persona/Views/Chat/PendingCards.swift` stacks the cards above the composer in `ChatView.swift`; with the keyboard up they take the conversation's room. Android: `Decisions.kt` in the design system draws the request in place of the composer, as a floating sheet with a grab handle in Glass, a docked panel in Brief and a strip under an amber rule in Board. `ChatScreen.kt` hides the composer, the queue strip and the source note while a request shows, shares one fold state across requests, and caps the height at 0.55 of the page. The assistant chat already shows its trays above a visible composer.

**Change.** In `ChatScreen.kt`, drop the gating and order the cards above the composer; fold each request on its own; cap the stack at about half the page, aware of the keyboard. In the design system: Glass without the handle and inset like the composer, Brief as a rounded card with a lighter shadow, Board as a bordered card. Correct `DESIGN_SYSTEM.md` section 4.8 to describe the stack.

Risks: room on a small phone with the keyboard and two text fields; what a typed message means while a permission blocks the Agent (a product decision); focus and TalkBack order; screenshots in all three styles.

Done (11-12), after Q4 was decided as follow iOS: in every style (the stack is behaviour), `ChatScreen` no longer hides the composer, the queue strip or the source note while a request shows, and `InteractionPanel` draws the cards in the design system's `DecisionStack`: in the order they came while they fit, past the cap scrolling under "N pending" with the permissions first and then the newest (`PendingStack.order`), a new card bringing the stack back to its top. Each card folds on its own (`RequestFolds`), from a tap on its header or its caret, to one line with its title and what it asks (`foldPreview`); the paging, "n / total" and the request list sheet are gone, and ↗ still opens a card's full page. The cap is half the page and never pushes the composer off it (an open tray takes its room first); with the keyboard up the cards take the conversation's room above the rest of the footer (`PendingStack.cap`, measured from the bars). Persona's conversation adds a second place of cards above its entries, the task question, which shares that room (`chats.md`, C10). A message typed while a permission waits is sent as any other, as iOS's `sendText` does. The looks, each in its style's `shapes.panel`: Glass a floating card without the handle, inset like the composer; Brief a rounded card with the composer's light shadow; Board a bordered card under its amber rule. Brief and Board change with the behaviour: their docked panel and strip only fitted in place of the composer. Tests `PendingStackTest`, `PendingStackUiTest`, `QuestionCancellationUiTest`, `DecisionSemanticsTest`; shots `PendingStackShotsTest`. Left: TalkBack and focus order checked on a device (the cards read top to bottom, as drawn); iOS's per-kind form caps with the keyboard up (`cardFits`), where Android caps the whole card at the stack's height and scrolls its body inside; the voice call's room (Android has no call bar in the chat).

### T7. Text size (if Q6 is yes)

**Today.** iOS: `apple/Persona/DesignSystem/TextSize.swift` moves Dynamic Type one step from the system's size, sizes that go with text scale up to 1.6 times, and the setting is in `apple/Persona/Views/Settings/AppearanceSettingsView.swift`. Android: text is in sp, while icons and controls are fixed dp; Settings › Interface style is `android/feature/settings/src/main/kotlin/persona/feature/settings/AppearanceSettings.kt`, and the style is stored through `AppStyle.kt`.

**Change.**

1. Store the size with the style, shown as a segmented control.
2. Apply it in each activity's `attachBaseContext`, next to the existing language override (`AppLanguages.kt`), moving the font scale one step along the system's ladder, and recreate the activity when it changes.
3. Scale icons, buttons, avatars and chips by the font scale, capped at 1.6, never below 48dp for targets.

Risks: overriding density inside the theme instead would compound and could lose Android 14's non-linear font scaling; fixed-height layouts may clip; the switcher already scales with font size and must not scale twice; the floating capsule's window needs the override too.

### Order of work

1. T2 and T1: small, independent PRs.
2. T3, after the motion tokens of phase 1.
3. T4, then T5.
4. T6 (Q4 was decided on 2026-11-12: follow iOS), then T7 after the owner answers Q6.

### Verification

- `scripts/app/android.sh test` for the list hold, the swipe component's actions and the size scaling.
- Recordings of each interaction next to the iPhone, light and dark; with animations off for T3 and T4.
- TalkBack: the Inbox actions without swiping, the selected text, the request cards' order.
