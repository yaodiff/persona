# screen-video

The live screen's video wire (#3954, `docs/extensions/computer-use.md` "Live screen video (#3954)"): the messages
Persona Screen and its clients exchange. The iPhone (`PersonaScreenKit`) and the browser (`web/src/screen/video-wire.ts`,
#6558) read the same cases, so both frame every message alike.

## Rules

- A message is a u32 big-endian length (the type byte plus the body), a u8 type and the body. Every case's
  `input.frame_hex` is one whole message. A length below 1 or over 4 MB is an error.
- Helper → client: `hello` 1, `format` 2, `frame` 3, `heartbeat` 4, `paused` 5, `resumed` 6, `timing` 7, `encoding` 8.
  Client → helper: `start` 16, `report` 17, `keyframe` 18, `update` 19. A type a side doesn't know is skipped by its
  length (`unknown_type`).
- `format`: a u8 count, then each parameter set as a u16 length and its bytes (HEVC's VPS, SPS and PPS; H.264's SPS
  and PPS).
- `frame`: the frame's number (u32), its capture time in µs (u64), flags (u8: 1 keyframe, 2 idle refinement), then the
  sample as length-prefixed NAL units (4-byte lengths).
- `report`: the last frame's number (u32), its capture time (u64, echoed), its arrival on the client's clock in µs (u64),
  the bytes since the last report (u32), flags (u8: 1 a decoding error).
- `keyframe` has no body; `resumed`'s body is `{}`; the rest are JSON objects with the field names in `expected`.
  A field a side doesn't know is ignored, and an absent optional field is left out, never `null`.
- The u64 values stay below 2^53, so a JavaScript number holds them exactly.

## Format

`messages.json` is `{"version": 1, "cases": [...]}`. Each case has `name`, `description`, `input.frame_hex`,
`expected` and `exact`:

- `expected.type` names the message. For a JSON message the other fields are its body's; for the binary ones they are
  `parameter_sets_hex` (`format`), `number`, `capture_micros`, `flags`, `sample_hex` (`frame`), `last_frame`,
  `capture_micros`, `arrival_micros`, `bytes_since_last`, `flags` (`report`), and `code`, `body_hex` for an unknown
  type. Hex is lowercase.
- `exact: true`: decoding `frame_hex` gives `expected`, and encoding `expected` gives `frame_hex` byte for byte.
  `exact: false` (JSON bodies): decoding gives `expected`; an encoding's key order and spacing may differ (the file's
  bodies have their keys sorted).

## Making it again

The cases are written from Swift values in `apple/Packages/PersonaScreenKit/Tests/PersonaScreenKitTests/ScreenVideoFixtureTests.swift`:
`PERSONA_WRITE_VIDEO_FIXTURES=1 swift test --package-path apple/Packages/PersonaScreenKit --filter ScreenVideoFixtureTests`.
Without the variable that test fails when the file is not what the Swift side writes now, so a wire change on either
side shows up in both languages' tests. All values are made up.
