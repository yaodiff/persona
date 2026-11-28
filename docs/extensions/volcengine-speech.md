# Volcengine speech (built-in extension `personad volc`, #8712)

For owners with a Volcengine account (its speech service, Doubao Speech / openspeech): one key, pasted on the phone, and meetings
transcribe on the hub with Volcengine's streaming recognition, and answers Persona gives in a meeting it takes part in are read aloud
in a Volcengine voice. Without a key nothing changes: on-device transcription, the GPT-Live adapter and the phone's own voice work as
before.

Who it serves: everyday owners who already pay for, or want, a Chinese-first speech service. The form shows one key, a voice and two
switches; models, voice ids and the old console's App ID are under **Advanced**.

Code: `server/internal/modules/volc` (the program), `server/internal/extmgr/volc_extension.go` (the manifest, the switch), the assistant's
`meeting_transcription_choice.go`; the stand-in service for tests is `server/internal/modules/volc/volctest`. The program
is the `volc` module (docs/architecture/core-and-modules.md "Volcengine speech"): a build with `persona_without_volc` or
`persona_core_only` has neither `personad volc` nor the extension.

## Setting it up

1. In the Volcengine console for Doubao Speech (`console.volcengine.com/speech/new`): sign in and complete identity verification.
2. On the overview page, **一键开通** (turn everything on; a new account also gets a free trial: 20 hours of recognition and 20,000
   characters of synthesis for each model generation). Otherwise, under **开通管理**, turn on **豆包流式语音识别模型2.0** and
   **豆包语音合成模型2.0**.
3. Under **API Key管理**, copy the default key (or create one).
4. In the app: **Settings › Extensions › Volcengine Speech**, turn it on, paste the key, **Save** (the key is tried on the service
   first, "Connection test"), then turn on **Use for Meeting Transcription**.

The extension must be on to save a key: the hub asks the running program to try the key before saving it (the generic `settings_check`,
`docs/extensions/extensions.md` "Settings the user fills in (#5906)"), and says so when it is off.

## The extension

A fresh host registers the built-in extension switched off. An existing package installed under the
ID `volc` keeps its place. Hosts with the built-in provider can preview a signed same-ID successor in
the official marketplace and roll back to the built-in after the first upgrade. See
`docs/extensions/extensions.md`, “Moving a built-in provider to the official marketplace”.
The built-in program is `personad volc` (and `persona-manager volc`), speaking the extension protocol (RPC on stdin / stdout, the
Go SDK's `ext.Main`) and serving HTTP on its socket. It says the build's version at `initialize`: the hub registered the built-in under
that version and runs the same executable.

| Contribution | Id | What |
| --- | --- | --- |
| `transcription` | `volc` | Meetings on the hub and extensions' own audio ("Recognition") |
| `speech` | `volc` | Meeting answers read aloud ("Synthesis") |
| `ui` (`extension_page`) | `status` | **Connection and Test**: each service's last outcome and **Test Connection** ("Connection test") |

The manifest is written in English with its translations (`volcTranslations`: name, description, the form, the key, the page's title).
It lists the one host the program reaches, `openspeech.bytedance.com`, under its network permissions. The key is a secret shown by
its last four characters, sealed on the hub (`plugin_secrets`) and handed to the program in `initialize` only.

### The form

| Field | Kind | Notes |
| --- | --- | --- |
| `meetings` | toggle, asks first | **Use for Meeting Transcription** ("One switch"): never saved with the settings |
| `read_answers` | toggle, on | **Read Answers Aloud**: off, the `speech` contribution says `setup: "off"` and the phone uses its own voice |
| `voice` | choice | Six synthesis 2.0 voices (`volc.Voices`), Vivi 2.0 first (it also speaks Japanese and other languages) |
| `advanced.asr_model` | choice | The recognition resource: 2.0 by the hour (the default), 2.0 by concurrency, 1.0 by the hour, 1.0 by concurrency |
| `advanced.voice_id` | text | Any voice id from the console's voice library, in place of `voice` |
| `advanced.app_id` | text | The old console's App ID: the key field then holds its Access Token |
| secret `key` | secret | The new console's API Key (or the old console's Access Token) |

### Credentials

The new console's API Key goes as `X-Api-Key` to both services. With an App ID (the old console), recognition gets `X-Api-App-Key` and
synthesis `X-Api-App-Id` (the two services name it differently), each with `X-Api-Access-Key`. Every request carries a fresh
`X-Api-Request-Id` (recognition also `X-Api-Connect-Id`); the service's `X-Tt-Logid` goes to the log, for its support.

## One switch

**Use for Meeting Transcription** is the assistant's meeting transcription choice, shown and set from the extension's settings, so the
owner turns everything on in one place and the switch can never disagree with Recording settings:

- The details show it as the choice is: on while `meeting_transcription_extension` is `volc/volc` (`volcDetail`, asking the assistant
  over `meeting.transcription_choice`).
- Saving the settings takes it out before they are stored (`volcConfigWrite`). When it differs from the choice: on makes `volc/volc`
  the choice (pinned to the installation, as `PUT /api/v1/assistant` does) and new meetings default to **On the hub**
  (`meeting_transcription_default: hub`); off returns a choice of `volc/volc` to the built-in one (`""`) and new meetings to the phone,
  and leaves any other choice alone. A save that changes only the switch stores nothing and restarts nothing.
- Turning it on asks first (`x-confirm`): new meetings transcribe on the hub, their audio goes to Volcengine as it is recorded, and the
  hub deletes it once the minutes are written.
- It is a change only the owner makes on a device (the settings routes need full credentials). The assistant's settings tool
  (`persona_settings` `extension.volc`) neither lists nor sets it, as it never sets the transcription choice
  ([meeting-minutes.md](../assistant/meeting-minutes.md) "Choosing the mode").
- `meeting.transcription_choice` (`assistantwire.MeetingTranscriptionChoose`) is the manager's call to the assistant in its process,
  not on the extensions' allowlist.

## Connection test

- **Saving a key or the settings** runs `settings.check` on the settings about to be saved: one recognition session given 0.6 s of
  silence and its last packet (billed by the second), and, while answers are read aloud, the synthesis of two characters. A service
  that calls the silence empty audio (`45000002`) has answered. Recognition must work; synthesis too while **Read Answers Aloud** is on.
  What the run in effect already did well is not tried again: another voice tries synthesis only; the switches alone try nothing.
- **Test Connection** on the extension's page tries the saved settings, at most once in 10 seconds, and answers in a toast.
- A refusal says what to do next, in the owner's language (`volc-check.why_*`): the key refused (copy one again from **API Key管理**),
  a model not turned on (turn **<model>** on under **开通管理**, the console's own name for it), a voice not allowed, the quota used
  up or the concurrent sessions taken (**我的资源**), no network from the hub machine. The hub takes every secret out of the words.
- The page shows each service's last outcome, from a test or from real use (a job's first session, a read-aloud), with the time to the
  first answer. Outcomes are kept in the extension's data directory (`status.json`) for the settings they were for (a fingerprint of the
  key, the App ID, the model and the voice), so the check that saved a key is on the page once the extension restarts with it.

## Recognition

The `transcription` contribution `volc` meets the contract of [meeting-minutes.md](../assistant/meeting-minutes.md)
"The `transcription` extension point": `/transcription/volc/info` answers protocol 1 with `live`, `replay` and `interim`, and
`setup: "key"` until a key is saved. A job is one WebSocket from the manager; the program stores nothing.

The service: `wss://openspeech.bytedance.com/api/v3/sauc/<endpoint>`, a binary protocol (a 4-byte header: version, message type and
flags, serialization and compression; a sequence number; a payload size; a gzipped JSON request or audio), PCM16 16 kHz mono in
packets of 200 ms, the last one flagged with a negative sequence number. The request: `model_name: bigmodel`, punctuation and number
normalization on, `show_utterances`, no language (the service hears Chinese, English and dialects mixed, so every word stays in the
language it was said in, as the contract wants).

- **Live** (`bigmodel_async`, the optimized two-way endpoint, with `enable_nonstream`): the service answers as it hears, and once a
  sentence ends (its voice activity detection, 800 ms) recognizes it again with the non-streaming model and marks it `definite`. Each
  definite utterance not handed on yet is a `sentence` (its times are session milliseconds, plus where the session's audio began); the
  last one not definite is the `interim` caption. `ready` goes out once the first session started, so a refused key or a model not
  turned on ends the job at once (`upstream_forbidden`).
- **Sessions**: one at a time takes new audio. A session ends where the audio does at a `flush` (its last sentences, then `flushed`),
  a hole (`seek` forward), a pause of 3 s (the service ends a session that gets no packet for 8 s) and the job's `end`; it rolls over at
  the first pause after 20 minutes (at the latest after 25: the service documents no limit). The next one starts with the next audio.
  A session that drops is replaced at once, then after 2 and 4 s, from the end of its last sentence, out of the job's last minute of
  audio (`warning session_dropped`); a fourth failure in a row ends the job (its kind's code below; `upstream_failed` before
  `ready` is `upstream_unavailable`). A session that drops while it finishes (its audio all sent) is not replaced: its last seconds are left to the
  phone's own text and the final pass. Sentences go out in order: a session's wait while an earlier one is still open.
- **Pace**: audio goes to the service as it comes, a burst (the manager's backlog after `ready`) at most twice real time. `ack` follows
  what the sessions were sent, every 5 s of audio.
- **Replay** (`bigmodel_nostream`, the streaming-input endpoint: more accurate, results once the audio ends): windows of about five
  minutes, ended at the first half second of quiet after that (at the latest 30 s more), at a hole and at `end`, each one session, its
  audio at the job's `speed`. A window's utterances are its sentences, then `window`; a window with no text where the transcript of
  record had some (6 characters or more, and one per 5 s) is `kept: true`, so the hub's live text stays. In a meeting's final pass (it
  has a transcript of record) a window that fails is tried once more; a second failure ends the job (the manager tries the pass again).
  In a job with no transcript of record (an extension's, "Transcription for extensions" in extensions.md) a window that fails is tried
  three times (at once, then after 2 and 4 s); after the third it goes out as `kept: true`, which the manager hands the extension as a
  gap, and the job goes on (#8887). Failures still count in a row across windows: a fourth with no window getting through in between
  (a service that is down) ends the job. A window waiting to be tried again holds back the windows after it, so they go out in order.
  The final pass bills about the meeting's length again.
- **Hotwords** (`request.corpus.context`, `{"hotwords": [{"word": …}]}`): the job's glossary (the meeting's title and calendar title,
  its attendees, then the owner's glossary) with the titles (the first two terms when longer than 8 characters or with a space) moved
  last: names and the owner's own terms matter most. Live sessions take what fits in the service's 100 tokens (a Chinese or Japanese
  character one, a Latin word one per four letters), leaving out terms over 20 characters; replay takes up to 5000 terms. The glossary
  is spellings only.
- **Usage**: the sessions' `audio_info.duration`, summed, in `usage` every 30 s and before `done`.
- **Codes**: a refused key, a model not turned on or a voice not allowed is `upstream_forbidden`; the quota, concurrency or a busy
  service `upstream_busy`; no network `upstream_unavailable`; anything else `upstream_failed` (#13446: the manager passes the kind
  on to the phone, which says why and what to do). The service documents few of its refusals, so its words
  are read too (`classify`: "not granted" is a model not turned on, "quota" and "concurrency" busy).
- **The log**: `transcription <n>:` lines for the job (its mode, language tag, glossary and hotword counts, model), each session (why,
  where, the service's logid), each drop and window, and the end with its counts. Never a sentence, a caption or a glossary term.

## Synthesis

The `speech` contribution `volc` (`docs/extensions/extensions.md` "Speech"): `/speech/volc/info` → `{"protocol": 1, "rate": 24000}`,
with `setup: "key"` before a key and `"off"` while **Read Answers Aloud** is off; `POST /speech/volc/speak` reads the text in the
settings' voice with the service's one-way HTTP endpoint (`https://openspeech.bytedance.com/api/v3/tts/unidirectional`, `pcm` at 24
kHz): lines of JSON, each with a piece of base64 audio, ended by code `20000000`; streamed to the caller as they come. A text longer
than 300 characters goes in pieces cut at sentence ends (the endpoint documents no limit; its older one takes 1024 bytes). The resource
follows the voice: `seed-tts-2.0` for the 2.0 voices (`*_uranus_*`), `seed-tts-1.0` for any other. The log line per text has the voice,
its length, the outcome, the bytes and the time to the first audio, never the text.

The iPhone's use of it (a meeting answer said in this voice before the voice extension's or its own) comes with the app's part of #8712.

## Relation to #1229

#1229 is Volcengine's full-duplex realtime dialogue (Seeduplex) as an upstream of the voice extension: calls and meeting calls. This
extension does one-way speech only and never touches the voice extension. The new console's one key covers recognition, synthesis and
Seeduplex: when #1229 is built, the voice extension's Volcengine dialect should take the key saved here, handed over by the manager as
a quota gateway's key reaches the voice extension today, so the owner enters it once.

## Privacy

- Only a meeting recorded with **On the hub** sends audio to Volcengine, as it is recorded, and only once the owner turned
  **Use for Meeting Transcription** on (or chose it in Recording settings): installing or turning on the extension sends nothing. The hub
  deletes a meeting's audio once its minutes are written ([meeting-minutes.md](../assistant/meeting-minutes.md) "Gaps and the final pass"). Extensions granted `transcription` send their own audio through the same choice.
- Reading an answer aloud sends its text to Volcengine.
- The key is never in a log, a reply, the repository or test data (tests use made-up keys with the stand-in service).

## Tests

- `server/internal/modules/volc`: the client against `volctest`, a stand-in written from the service's documentation independently of the
  client (its own framing): live jobs (captions, sentences and their recording times, flush, end, usage, the request and its hotwords),
  a dropped session replaced without a sentence said twice or out of order, a hole, a pause, refusals (`upstream_forbidden`,
  `no_upstream`), replay windows (kept ones; a failing window as a gap in an extension's job, a service that goes down, a final pass
  that still ends), the old console's headers, synthesis and its errors, the settings check (what it tries, its
  words), the page.
- `server/internal/extmgr` `TestVolcSetup`, `TestVolcMeetingsSwitch`: the hub runs the real program (the test binary as `volc`) against
  the stand-in (`PERSONA_VOLC_SERVICE`): off until turned on, needs setup without a key, a refused key not saved, a key checked, sealed
  and handed over, the transcription's `/info`, speech through the apps' forward, the page's title in the request's language, the switch
  and the assistant's choice.
- `server/internal/assistant` `TestMeetingTranscriptionChoice`.
- Nothing here calls the real service. A real check (latency, errors, which contribution transcribed) is done on a device once the owner
  has saved a key.
