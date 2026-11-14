//! Persona's client logic without IO, clocks or FFI (docs/plans/plan-rust-core.md). Everything comes in as an event and
//! goes out as state and requests, so the shared fixtures (`spec/fixtures/`) replay against it directly.

#![forbid(unsafe_code)]

pub mod continuity;
pub mod item;
pub mod recents;
pub mod sort;
pub mod state;
pub mod transport;

pub use item::Item;
