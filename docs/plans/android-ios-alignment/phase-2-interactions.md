# Phase 2: interactions (#4210)

Part of the [Android alignment plan](README.md) (#4194). Status: T6 done (11-12) and T1, T2, T3 and T5 (11-13); T4 left out for now and T7 waiting for the owner's answer to Q6 (see their sections).

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

Android: `android/feature/inbox/src/main/kotlin/persona/feature/inbox/InboxItemRow.kt` puts Done or Move Back on the start side and the hand-off on the end side, and the long-press sheet offers the hand-off. The row component, `SwipeActions.kt` in the design system, wraps Material's swipe-to-dismiss: full swipes only, no accessibility actions, and color only through a status.

**Change.**

1. In both row files: no start action; the end action is Done, or Move Back with the undo-arrow icon when the item is done, and none while busy or picking. Rows in the done section get Move Back on the same swipe.
2. The component gets a tone (accent or positive) and accessibility actions, so TalkBack users reach the same actions without swiping.
3. The haptic follows iOS: a tap for a single item (today Android plays success).
4. The hand-off stays in the long-press sheets for now; #3901 replaces it with forwarding.

Risk: with gesture navigation, a swipe that starts at the very right edge is the system's back. Starting inside the row works, as in other Android apps with row swipes.

Done (11-13): in every style (direction and actions are behaviour), both row kinds (`InboxItemRow`, `InboxConversationItemRow`) take one swipe, from the end, from `inboxRowSwipe`: **Done** (the check mark) in the positive tone, or **Move Back** (the undo arrow) in the accent tone on a done item and in the Done section; none from the start, none while the row's write is out or items are being picked. The hand-off is only in the long-press menus now (an open row's long press starts a selection, and the picked row's More holds Hand to AI, as before); #3901 and #6042 are untouched. `JSwipeActions` gets a `SwipeTone` and TalkBack actions: every action of a swipe is a custom accessibility action on the row that applies `swipeActionSemantics()` (`MessageItem` does, in its Glass card too), since only the row's own node takes the screen reader's focus. On Glass a toned action is drawn as iOS tints it: a fill of the accent or the positive green with white marks (black on the bright green in dark, where white would be 2:1); Brief and Board keep their neutral backing and the status color, so their rows are pixel-identical at rest (`android_shots_diff.py` of `main` against the branch: the whole Inbox page at rest, three styles, light and dark, 6 of 6 identical). A single item gives a tap haptic both ways, a conversation row counting as one; many at once keep success when filed (`doneHaptic(done, single)`). The label for putting back becomes iOS's **Move Back** (移回, 戻す) in all three languages, and the long-press sheet shows the same done and undo icons. Tests `InboxRowSwipeTest` (the action of each state, open, done, busy and picking, for both row kinds; the gesture each way; TalkBack's actions; the fill on Glass and the neutral backing in Brief and Board), `SwipeActionsAccessibilityTest` (the component's actions, the latest function, the tone's colors), `InboxConversationSwipeTest` (the conversation tests moved to the one swipe, with the hand-off through the long press and the tap haptic), `InboxHapticsTest`; shots `InboxSwipeShotsTest` (the revealed swipe on Glass, light and dark, `main` beside the branch; Brief and Board at rest and revealed). Release notes: no file in the repository keeps them for Android; they are compiled from merged PRs, so the PR's "Release notes (Android)" paragraph says it (principle 1). Left: the other rows inside `JSwipeActions` (To-dos, Agents, the conversation history) do not apply `swipeActionSemantics` yet, so TalkBack still reaches their swipes through their own menus; iOS's full swipe is a threshold on Android, as for every swipe row (56 dp).

### T2. Dismissing the keyboard on drag

**Today.** iOS hides the keyboard when a real drag starts on the conversation (in `apple/Persona/Views/Chat/ChatView.swift`; the flipped list ignores SwiftUI's own setting). Android's chats share a reversed `LazyColumn` in `android/core/chatlist/src/main/kotlin/persona/core/chatlist/ChatList.kt`; only a tap on empty space hides the keyboard (`KeyboardDismiss.kt`), and a drag is consumed before it gets there.

**Change.** `ChatList.kt` already collects drag interactions; on a drag start, clear the focus and hide the keyboard. An interactive keyboard that follows the finger is not worth its custom code. Like iOS, a drag also takes the focus from a field in a request card.

Done (11-13): in every style (a drag is behaviour), `ChatListEffects` clears the focus and hides the keyboard when the list reports a `DragInteraction.Start`, which only a finger past the touch slop starts: the two calls the page's tap on blank space already makes (`dismissKeyboardOnBlankTap`). Like iOS's `Keyboard.dismiss()` it takes the focus from any field, so the field of a request card goes too, since the pending stack's cards sit outside the list. The Agent chat and Persona's conversation (`ChatScreen`) and the Lark chats (`LarkScreen`) all draw `ChatList` under `ChatListEffects`, so the one change covers the three. A tap on blank space still puts the keyboard away, once; the field, a link and a long press leave it; a scroll the page starts (following the newest message, a jump, a search hit) is not a drag and leaves it. The keyboard does not follow the finger, as planned. Tests `ChatKeyboardDismissTest` (the list under a focused composer, in every style: a drag each way, a card's field, a scroll the page starts, a tap, a long press), `ChatDragKeyboardTest` (the real Agent chat and Persona's conversation, with a request card too, and a message arriving while someone types) and `LarkDragKeyboardTest`; with the change taken out, the eight drag tests fail and the rest pass. No screenshots: nothing changes in how anything looks. Left: a soft keyboard on a device or emulator (Robolectric has none and is not in touch mode, so the tests check what the page asks of the focus manager and the keyboard controller); the Lark thread sheet (`LarkThreadSheet`), whose reversed list is its own and not the shared `ChatList`, keeps its keyboard when dragged.

### T3. The send button

**Today.** iOS (`apple/Persona/DesignSystem/ShellComposerBar.swift`, `apple/Persona/DesignSystem/ComposerButtons.swift`, curves in `apple/Shared/Design/Theme.swift`): send grows from a scale of 0.001 at the right edge while a 4 pt blur clears, fading in over 0.2 s; stop and voice shrink to 0.2 and fade; the swap follows the curve (0.38, 0.7, 0.125, 1) over 0.4 s; a press shrinks send to 0.75. With Reduce Motion it is a plain state change. Android (`android/feature/chat/src/main/kotlin/persona/feature/chat/ChatComposer.kt`) swaps send and stop with a plain `when` and has no press scale.

**Change.** Wrap the swap in `AnimatedContent`: scale in from 0.001 with the origin at the right edge, over 400 ms on that curve, with a 200 ms fade, against a scale out to 0.2 with a fade. Blur with `Modifier.blur` on Android 12 and later (nothing on Android 11). Shrink on press through the pressed state. Snap when the system's animations are off. The durations and the curve come from the motion tokens (phase 1, T3).

Risks: the leaving button stays clickable during the animation, so disable it as it leaves; the trailing slot moves between rows when the composer expands, which restarts the animation.

Done (11-13): on Glass the composer's send and stop move as on iOS, in every chat composer (Agents chats, Persona's conversation and New chat all draw it through `ChatComposer`). `ComposerActionSlot` (`core/designsystem`, `ComposerAction.kt`) shows the one round button through `AnimatedContent`, following the iOS code where the plan's wording is shorter: send grows out of the trailing edge from a scale of 0.001 through a 4 dp `Modifier.blur` (Android 12 and later, none on 11) and fades in over 0.2 s, and send leaving does the same the other way (`composerSend` is symmetric); stop (and voice, which Android has no button for) shrinks to 0.2 from its middle as it fades (`composerSwapped`); scale and blur run on (0.38, 0.7, 0.125, 1) over 0.4 s. The slot's width glides with the state spring so the mic and model pill beside it do too, as iOS's card does on `canSend`. The button on its way out is disabled and hidden from TalkBack but keeps the look it left with (what it draws is the state, its taps are read when they come). `SendButton(pressScales = true)` shrinks to 0.75 under a finger (0.4 s easing out, back over 0.25 s) for the composer's send only, as iOS's `pressScales`; the Inbox and Lark composers' send keeps the plain press. With the system's animations off (`PersonaTheme.reduceMotion`) nothing moves, where iOS still scales and blurs (HIG-044); Brief and Board swap at once, as before. `ComposerFrame` keeps its trailing content's state across its move between the two rows on Glass (`movableContentWithReceiverOf`): without it even a first word typed into a composer that was not yet open ended the swap in its last frame. The values are `composerSwap` and `composerPress` in `spec/tokens/motion.json`, generated into `Tokens.Motion` and checked against `Theme.Motion` by `test_gen_tokens.py`. Tests `ComposerActionSlotTest`, `SendButtonPressTest` (`core:designsystem`) and `ChatComposerSwapTest`; frames `ComposerSwapMotionShotsTest` at 0, 40, 80, 120, 200 and 400 ms, light and dark, Persona's composer typing and an Agent's stop turning into send, on `main` and on the branch, and once with animations off. Of the 232 pictures the existing composer shot tests draw (all three styles, light and dark), 214 are pixel-identical to `main`; the other 18 (`NewChatMentionsShotsTest`) differ only in the top bar's glass button, which differs the same way between two runs of the same code. Left: iOS's Voice Chat button has no place in Android's slot until it has the button (phase 3), and the slot takes a third kind then; the swap was not recorded on a phone's GPU, so its blur and glide are the Robolectric render's; no iPhone recording beside it.

### T4. Unfolding in place

**Today.** iOS: `.unfolding` in `apple/Persona/Views/Chat/ChatPin.swift` reveals new content with a mask that grows downward plus a fade (off with Reduce Motion), over 0.28 s ease-out, and conceals over 0.2 s ease-in. `toggleInPlace` applies the height change at once, without animation, and `ChatPin.hold` scrolls the list on every layout pass for up to 1.5 s, so the tapped header stays still. It is used in 12 files. Android: work steps are separate items of the reversed list in `ChatScreen.kt`, so steps inserted under the header push it up; thoughts and tool calls grow inside their rows.

**Change.**

1. A hold in `core/chatlist`: before the toggle, read the header's offset; then keep it with `requestScrollToItem(index, offset)` until the layout settles, 1.5 s pass or the user drags. Pause the follow-the-end scrolling meanwhile.
2. Reveal new items with a clip and a fade drawn in `drawWithContent`, without animating heights. Closing conceals first and removes after.

Risks: offset arithmetic in a reversed list; a header that is off screen; steps streaming in while open; the saved reading position during a hold.

Not done (11-13): left out of this round, decided on 2026-11-13. The design above stands for when it is picked up; until then the rows of an opened step or tool call still push its header up.

### T5. Selecting in place

**Today.** iOS (`apple/Persona/Views/Chat/LiftSelection.swift`) redraws the lifted message in a text view, because its Markdown renderer can't select. Android's Select opens `FullTextReader.kt`, full screen, with the raw Markdown.

**Change.** The Markdown renderer Android uses draws with Compose `Text`, so one message can be wrapped in a `SelectionContainer`. Select makes only that message selectable and turns off its long press, its swipe to quote and its inline-code copy until the selection ends. The Lark views already select in place with their own menu items (`android/feature/inbox/src/main/kotlin/persona/feature/inbox/LarkTextPicks.kt`), and a Quote item can reuse that.

Risks: no selection across messages; a selection may be lost when the message scrolls away; Compose may have no public way to start with the word under the finger selected (to check); inline math images and scrolling code blocks.

Done (11-13): in every style (what Select does is behaviour), choosing Select in a message's menu selects the words of that message where it is. `SelectableWords` (`MessageSelection.kt`) keeps the message's words in one `movableContentOf`, so an opened quote or an unfolded code block keeps its state, and moves them into a `SelectionContainer` with `selectAll()` once they have registered: the system's handles, its long press that picks a word and its floating menu (Copy, Select all, the platform's own items) work on them, and a Quote item is added to that menu (`appendTextContextMenuComponents`, as `LarkTextPicks.kt` does). Quote takes only the picked words, as iOS's `ContextRef.quote(part: "selection")` does: a composer card "Quoting selected text · agent" (`ChatLinkContext.part`, `part="selection"` in the XML, saver V3 that still restores V2 and older), kept once per message and words. While it selects, the message has no long press, swipe to quote or inline-code copy (`messageMenu` returns at once; `LocalSelectionAbove` makes `copyingCode` draw plain text); its code blocks are selected with it (a nested `SelectionContainer` would drop them from Select all: `SelectableOnItsOwn`), while their header and copy buttons, the streaming cursor, persona views and cards are `DisableSelection`. It ends as iOS's does when nothing is selected any more (a tap on the words, Copy, Quote) and, as Android's own text selection does, on a tap that starts outside the message (`endSelectionOnTapOutside` on the chat list: it only watches, so the tap goes on to what is under it, and a drag scrolls the list with the words still selected), on Back, and when the message has left the screen or the composition; choosing Select in another message moves the selection there. Copy goes through `pickedWords`: no thin spaces from the inline-code padding, no placeholders for formulas, a list item's marker back on its line. `FullTextReader` stays where else it opens (a long message's "View full text" and reader button, a code block's reader, thoughts, plans, a tool's raw input and output); Select no longer opens it. Tests `MessageSelectionTest` (19: selection on for that message only, long press, swipe and inline-code copy off and back, Copy, Quote and its part, tap outside, a drag, Back, scrolled away, unfolded state kept, your own bubble, a message with nothing to select, no reader, the part surviving a configuration change, the card's heading); shots `MessageSelectionShotsTest` (at rest and selecting, three styles, light and dark: at rest Brief and Board are pixel-identical to `main`, and selecting moves nothing but the highlight). Checked on an emulator: handles and floating menu, Quote, Copy to the clipboard, Back, a tap outside, a long press that picks a word, scrolling with the words selected. Left: starting with the word under the finger (Compose has no public way: `SelectionState` offers `selectAll`, `select(range)` and `extendSelectionByWord`, and the Markdown renderer exposes no text layouts to turn a point into an offset), so Select selects the whole message and a long press on a word picks that word, as iOS's second long press does; iOS's lift of the message over the dimmed chat, which an action sheet over the chat does not need; formulas are pictures and are left out of what is copied (the Markdown model cannot say their source from here); a table copies one cell per line; Robolectric draws neither the handles nor the system's menu, so the shots show the highlight only (the emulator shows both); TalkBack on a device is not checked.

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

Not started (11-13): Q6 is still open with the owner (#4208), and T7 was left out of this round on 2026-11-13.

### Order of work

1. T2 and T1: small, independent PRs.
2. T3, after the motion tokens of phase 1.
3. T4, then T5.
4. T6 (Q4 was decided on 2026-11-12: follow iOS), then T7 after the owner answers Q6.

On 2026-11-13 T2, T1, T3 and T5 were built in that order (#6606, #6633, #6665, #6669); T4 and T7 were not.

### Verification

- `scripts/app/android.sh test` for the list hold, the swipe component's actions and the size scaling.
- Recordings of each interaction next to the iPhone, light and dark; with animations off for T3 and T4.
- TalkBack: the Inbox actions without swiping, the selected text, the request cards' order.
