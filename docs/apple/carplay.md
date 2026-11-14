# Long voice conversations on CarPlay

Talk with Persona in the car (full duplex, interrupt at any time). Siri is only the entry point; the call uses the shared `VoiceConversation(surface: .carPlay)` / `VoiceCalls` engine (`docs/assistant/voice.md` section 6, #3789), bound to the assistant's main chat.

## Using it

- Tap Persona's icon in CarPlay, or tell Siri **Open Persona**: the app starts listening directly, without a button press first.
- Tell Siri **Talk to Persona** (`TalkToPersonaIntent`): it starts the conversation if Persona is on the car's screen. If CarPlay is connected but another app (such as Maps) is on screen, it only says to open Persona on the car's screen first, without opening the microphone; without CarPlay, it explains that you must connect first (`CarPlayTalk`).
- Screen buttons: **End / Interrupt / Continue**.
- Leaving Persona's screen (for example switching to Maps) stops it: Apple requires the voice-control interface on screen during a voice conversation. Returning to Persona resumes the conversation.
- It auto-starts once after connecting to CarPlay, and once each time it returns to the foreground (`CarPlayAutoStart`). Notification banners, Siri and the end of a phone call only deactivate / reactivate the interface, without reopening the microphone. After **End**, an idle stop, or ending because permission / a voice extension is missing, only **Continue** or Siri can start it again.
- If microphone permission is undecided, no prompt is shown (it would appear on the phone in your pocket, leaving the car stuck at **Connecting**). Instead, end and explain that you must first use dictation or recording on the phone to allow the microphone (`CarPlayMicPermission`).
- Stops automatically after 10 minutes without anyone speaking (the engine's idle rule).

## Full-duplex entry

CarPlay is a thin shell around the shared engine (`CarPlayVoiceSession`):

1. Resolve the assistant's main chat (`AppModel` or `IntentSupport` + `assistant()`) and the first `running` voice extension (`VoiceAvailability.pick`).
2. `VoiceCalls.start(..., surface: .carPlay, deps: VoiceDeps.live`, with `mic` replaced by the non-prompting `CarPlayMicPermission`).
3. The engine connects to `/api/v1/extensions/<id>/ws` through `extensionPath` (old hubs still use `/api/v1/plugins`), sends protocol 2's `start`, receives `ready`, then `PhoneVoiceAudio` handles the microphone and playback.

The hub's config selects the real-time service. When work is needed, the extension sends it to the bound main chat; the fast layer relays the result aloud (`docs/assistant/voice.md`).

## Audio

- Shares `PhoneVoiceAudio` with phone voice calls: `.playAndRecord` + `.voiceChat`, echo cancellation, dropping audio from before `clear` by generation.
- Activates the audio session only during a conversation; ending uses `notifyOthersOnDeactivation` so music resumes.
- Shares microphone ownership with recording and dictation (`RecordingMicrophone`); either ends the call.
- System interruptions such as incoming calls end the conversation; it never quietly continues recording in the background.
- "End on backgrounding" applies only to `surface == .phone`; CarPlay stops when it leaves its own voice screen (`VoiceCalls.end(.left, from: .carPlay)`).

## Apple's restrictions

Requirements from the CarPlay Developer Guide checked in 2026-11:

- The entitlement is `com.apple.developer.carplay-voice-based-conversation`, requires iOS 26.4 or later, and needs Apple's approval.
- Voice-conversation templates have at most 3 levels.
- Voice on launch: start listening when entering the app.
- The voice-control interface (`CPVoiceControlTemplate`) must be visible while voice is active.
- Hold the audio session only while using voice.
- Do not display answer text or images; answers in the car are audio-only.
- Recording is allowed only with the voice-control template.

## Enabling it

Do not enable before approval, or signing fails.

1. Apply for the CarPlay voice-conversation entitlement on Apple's developer site (the CarPlay application page). After approval, enable it for Persona's App ID.
2. Regenerate the release provisioning profile (`docs/operations/release.md`).
3. Set `"app": {"carplay_voice": true}` in config.json, then run `python3 scripts/persona_config.py xcconfig`. It generates `apple/PersonaOptIn.entitlements` (git-ignored, shared with the other opt-in entitlements) and makes Persona sign with it.

When disabled, Persona does not appear in CarPlay; everything else is unchanged.

## Compile guards

- `CPVoiceControlState.actionButtons` is a Swift property introduced in the iOS 26.4 SDK. **Do not wrap it in `#if compiler(>=6.4)`**: Swift 6.4 appears only in Xcode with the iOS 27 SDK, while Xcode 26.x (Swift 6.3) with iOS 26.4 or newer SDKs also has the property. That guard would remove the buttons from every Xcode 26 build.
  It is set through the Objective-C runtime (`CarPlayVoiceButtons`: `responds(to: setActionButtons:)` + KVC), compiling with any SDK and showing buttons from iOS 26.4 onward (the systems supporting voice templates). `CarPlayVoiceButtonsTests` scans the source to prevent regression.
- If the property is missing at runtime, there are no buttons; the second line of **Ended** tells you to use Siri's **Talk to Persona** to resume.
- Only Xcode 27.2 (iOS 27.2 SDK) has been verified by an actual build; Xcode 26.x has not.

## Verification

- Simulator: Xcode › Open Developer Tool › Simulator, then I/O › External Displays › CarPlay (or the CarPlay Simulator app).
- Without the entitlement, Persona does not appear in CarPlay.
- Unit tests: `CarPlayVoiceScreenTests`, `CarPlayWiringSourceTests`, `CarPlayVoiceButtonsTests`; engine tests: `VoiceConversationTests` / `VoiceCallsTests`.

## Not implemented / deferred

- The phone's voice-call interface (#3508).
- Model and voice selection: use the extension defaults first (`docs/assistant/voice.md`).
- Continuing while another app (such as Maps) is in front: Apple requires the voice interface on screen.
- Dashboard and steering-wheel-button controls.
