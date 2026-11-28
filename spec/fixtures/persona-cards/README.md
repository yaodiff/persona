# Persona's pending cards

`cases.json` pins D9 and D11 of `docs/plans/plan-derived-counts.md` for a single manager. The inputs are that
manager's decorated sessions and pending interactions. `muted` lists the session keys (`machine_id/session_id`)
whose agent or space is muted after applying any exceptions. Interactions are oldest first. The expected
`card_ids` name the counted cards in order. Each gives one `card:<id>` key and adds one to `n` and its total.

The asking session determines membership even when a child or side is folded under a different row. A form counts
as a card, as does a pending card that has yet to ring. Directory hiding overrides quiet membership. A named
session absent from the inventory has no known role or visibility. A card with an empty session id belongs to
`agents_needs_you`. A display copy contributes nothing, while its original remains eligible with `mirror_main`.

`TestPersonaCardsViewFollowsTheSharedFixture` in the fleet count adapter replays every case, including a known zero.
The counts view samples also include the case with Persona cards, a worker and a sessionless card.
