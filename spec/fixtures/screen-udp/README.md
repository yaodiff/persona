# screen-udp

The live screen's video over UDP (#7873, `docs/extensions/computer-use.md` "Video over UDP (#7873)"): the packets a
phone and the node's `internal/screenlink` exchange on a UDP flow, how a video message is cut into Reed-Solomon blocks,
and the parity itself. Both sides read the same cases, so they seal, number, cut and rebuild alike.

## Rules

- **Keys**: the TLS 1.3 exporter of the direct route's connection, with the label `EXPORTER-persona-screen-udp-1` and
  the flow's 8-byte id as the context, gives 88 bytes: node → phone key (32) and IV (12), then phone → node key (32) and
  IV (12). `exporter.material_hex` stands in for an exporter's output.
- **A packet**: a clear header (u8 version 1, the 8-byte flow id, u64 big-endian packet number), then the body sealed
  with AES-256-GCM: the direction's key, the nonce the direction's IV with the packet number XORed into its last 8
  bytes (big-endian), the clear header as additional data, the 16-byte tag at the end. `sealed_hex` is the whole packet.
  Each direction numbers its packets from 0 and never repeats one; a receiver drops a number it has seen or one older
  than its 4,096 newest.
- **Bodies** (`body_hex`) start with the kind. Integers are big-endian.
  - `hello`: the phone's is u64 the challenge it answers (0: none) and zero padding to 32 bytes after the kind; the
    node's is u64 a challenge (0: none) and u8 validated.
  - `data`: u8 channel, u64 the message's offset in the channel, u32 its length, u16 block, u16 index (below `data`: a
    data shard; from it on: parity), u8 data shards, u8 parity shards, u8 flags (1: a frame, which may be given up),
    then the fragment.
  - `ack`, `skip`, `reset`: u8 channel, u64 offset.
  - `nack`: u8 channel, then entries of u64 message offset, u16 block, u16 index.
  - `want`: u8 channel, u64 from, u64 to.
  - `use`, `resumed`: entries of u8 channel, u64 offset, as on the link.
  - `ping`: u64 the phone's clock (µs), u16 parity per data shard in ten-thousandths, u32 pacing in kbit/s (0: the
    node's).
  - `pong`: u64 the ping's clock, then u64 data, parity and resent bytes sent so far (whole packets).
- **Layout** (`layout`): a message is cut into shards of 1,146 bytes, at most 128 data shards to a block. A block's
  shards are 1,146 bytes long, or the message's length when it is one short shard; the message's last data shard is
  padded with zeros to the block's length for the parity and sent unpadded. A block of k data shards gets
  ⌈ratio × k / 10,000⌉ + 1 parity shards at a ratio above 0 (none at 0), the ratio capped at 4,000, and at most
  256 − k.
- **Reed-Solomon** (`reed_solomon`): systematic, over GF(2⁸) with the polynomial x⁸ + x⁴ + x³ + x² + 1 (0x11d). Parity
  shard i of a block with k data shards is the sum over data shards j of c(i, j) × shard j, with
  c(i, j) = 1 / ((k + i) XOR j). Any k of the k + m shards rebuild the data: each case lists shards (`lost`) a decoder
  must do without.
- **Link** (`link`): `udp_open` (type 12, empty) and `udp_ready` (type 13, JSON `port` and `flow`, or `unavailable`)
  travel on the route's TLS connection, framed as every link message (`spec/fixtures/screen-link`).

## Format

`cases.json` is `{"version": 1, "exporter": {…}, "packets": […], "layout": […], "reed_solomon": […], "link": […]}`.
A packet case has `name`, `description`, `direction` (`node_to_phone` or `phone_to_node`), `flow_hex`,
`packet_number`, `body_hex`, `sealed_hex` and `expected` (the body's fields, with `kind`). Encoding `expected` gives
`body_hex`, sealing it gives `sealed_hex`, and opening `sealed_hex` gives `body_hex`, byte for byte. All values are
made up.

## Checked by

- Go: `server/internal/screenlink` `TestTheUDPWireMatchesTheSharedCases` (`go test ./internal/screenlink`).
  `PERSONA_WRITE_UDP_FIXTURES=1` writes `cases.json` from the test's cases.
- iOS: `apple/Packages/PersonaScreenKit`, with the phone's side of the flow.
