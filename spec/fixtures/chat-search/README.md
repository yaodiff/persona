# What chat search finds

One rule for what is searchable in a chat, shared by every client and the hub's managers
(`docs/design/proposals/assistant-search.md`, section 2): what the chat can show, including behind a disclosure, and
nothing it never shows.

| File | Covers |
| --- | --- |
| `cases.json` | the user's words, a quoted context, a card's answer (both forms of its hidden tag), view errors, photos, a file card, pasted text, injected messages, the assistant's reply, its Markdown (indexed without the markup, #7072; bold around code, across lines or without a partner, #7195) and card, a thought, a tool call and its output, a notice |

- Shape: `{"version": 1, "cases": [...]}`. `item` is an item as the hub sends it to the apps (an injected message carries
  the hub's `injected`, line included, since a client's own fallback reads the line in its UI language). `fields` is
  what search in chat matches, in order: `[group, text]`, the groups being `text` (messages), `quoted`, `thinking`,
  `tool_name`, `tool_input`, `tool_output` and `event`. `index` is what the assistant's chat index keeps of the item
  (the user's words and the assistant's replies only), or `null` when it keeps nothing.
- Snippets are cut from these texts only, so no hidden tag can show in one, nor a Markdown marker of a reply (the index keeps a reply as the chat shows it: `searchPlain` in `server/internal/assistant/search_text.go`, `search_plain` in the mock).
- A card is its fallback text, as `AssistantCardText` in `sdk/go/wire` makes it; the cases only use cards that have one
  (without one, the hub writes a placeholder and the web app nothing).
- Read by the web app (`web/src/lib/chat-search.test.ts`), the managers' index (#6282) and Android (`ChatSearchTest`, #6285).
