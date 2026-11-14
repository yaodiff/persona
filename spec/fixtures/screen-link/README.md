# screen-link

The live screen's session wire (#6398, `docs/extensions/computer-use.md` "Live screen routes (#6398)"): the messages
the phone and the node's `internal/screenlink` exchange on every transport of a screen session. Both sides read the
same cases, so the iPhone's client and the node frame every message alike.

## Rules

- A message is a u32 big-endian length (the type byte plus the body), a u8 type and the body, as on the video wire.
  Every case's `input.frame_hex` is one whole message.
- `data`, `ack`, `close` and `reset` start with the channel (u8) and an offset (u64, big-endian); `data` carries its
  bytes after that. `use` and `resumed` are a run of such 9-byte entries, in the order given (`use_two_channels`,
  `resumed_empty`).
- `ping` is a u64; `pong` is that u64, then u32 packets retransmitted and u32 packets sent (`ping`, `pong`).
- `paths` and `attach` are JSON. `paths` may leave `cert`, `port` and `unavailable` out; `candidates` is always an
  array (`paths_direct`, `paths_firewall`).
- A type a side doesn't know is skipped by its length (`unknown_type`).

## Format

`messages.json` is `{"version": 1, "cases": [...]}`. Each case has `name`, `description`, `source`, `input.frame_hex`
and `expected`, and `exact`:

- `expected.type` names the message; the other fields are its contents: `channel`, `offset`, `bytes_hex` (lowercase
  hex), `entries` (`[[channel, offset], ...]`), `value`, `retransmitted`, `sent`, `paths` (the JSON object), `token`,
  and `code` for an unknown type.
- `exact: true`: decoding `frame_hex` gives `expected`, and encoding `expected` gives `frame_hex` byte for byte.
  `exact: false` (JSON bodies): decoding gives `expected`; field order and spacing of an encoding may differ.

All values are made up; addresses are from documentation and shared ranges.

## Checked by

- Go: `server/internal/screenlink` `TestTheWireMatchesTheSharedCases` (`go test ./internal/screenlink`).
- iOS: `apple/Packages/PersonaScreenKit` `ScreenLinkWireTests` (`swift test` in that package).
