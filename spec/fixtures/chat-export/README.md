# Exporting a conversation

A conversation exported as Markdown (#7375): the web app's **Export Conversation…** writes it (`web/src/lib/chat-export.ts`,
`transcriptMarkdown`), and the apps are to write the same file. How the web reads the whole conversation and what the
dialog offers are in [web.md](../../../docs/web/web.md), "Exporting a conversation".

## Rules

- First the title on one line (`# …`), then the agent, the computer, the folder and when it was exported, each as
  `- <word>: <value>` and left out when empty. `conversation`
- A document that stops before the conversation's start (the reader's limit) says where it begins, as a quote. `partial`
- Each part under `## <who> · <time>`: you, Persona for a message it typed in (`injected`: its line, then its detail),
  another session by its name (`from`), the agent for its replies. One message's items (its words and pictures, the same
  `client_msg_id`) share one heading; the agent's replies in a row share one. `conversation`
- A notice is its own italic line between the parts; folded work (`fold`) and the while-you-were-away recap (`recap`) are
  left out, and so is a text with nothing in it. `conversation`
- A picture is `_[Image]_`. Text the hub still carries cut (`full_size`) ends with a line saying how much is left out.
  `conversation`
- Without work steps only what was said is written: thinking, tool calls and their results are left out. `work_off`
- With work steps, they go inside the agent's part in order: thinking in `<details>` titled Thinking; each tool call in
  `<details>` titled with its name and what it was given (the first of `command`, `pattern`, `file_path`, `path`, `url`,
  `query`, `description`, `prompt`), its input as indented JSON and its result (Error when it failed) in fences, at most
  4000 characters each and a line saying how much more there was. A result whose call is not in the document gets its
  own block. Text in a `<summary>` escapes `<`, `>` and `&`. `work_steps`
- A fence is longer than any run of backticks inside it, at least three. `work_steps`

## Cases

`cases.json`: the words (English), the meta, and per case the items as `GET …/items` gives them, whether work steps are
in, whether it stopped at its limit, and the Markdown expected. Times are the item's `ts` in UTC as `YYYY-MM-DD HH:MM`
(the apps show their own time zone).
