//! A chat's continuity: which lines it has, whether they are complete, and what to fetch
//! (`spec/fixtures/continuity/README.md`, `docs/protocol/protocol.md`).

mod reducer;
mod sync;

pub use reducer::{Batch, Gap, Reducer};
pub use sync::{MAX_FILL_PAGES, MAX_RANGE_PAGES, Request, Session, Step};
