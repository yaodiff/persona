//! The on-device recent-conversation and recent-setup lists (`spec/fixtures/recents`).
//! These functions only transform their inputs; storage and the current time belong to the clients.

use std::collections::HashMap;

use serde::{Deserialize, Serialize};

pub const MAX_RECENT_CONVERSATIONS: usize = 10;
pub const MAX_RECENT_SETUPS: usize = 20;
pub const MAX_RECENT_SETUP_CARDS: usize = 3;

/// The conversation just opened goes first, once. An empty key is ignored.
pub fn open_conversation(before: &[String], opened: &str) -> Vec<String> {
    if opened.is_empty() {
        return before.to_vec();
    }
    std::iter::once(opened.to_owned())
        .chain(before.iter().filter(|key| key.as_str() != opened).cloned())
        .take(MAX_RECENT_CONVERSATIONS)
        .collect()
}

/// Offer existing conversations, newest first, without the one already on the page.
pub fn shown_conversations<F>(entries: &[String], current: &str, existing: F, limit: usize) -> Vec<String>
where
    F: Fn(&str) -> bool,
{
    entries
        .iter()
        .filter(|key| key.as_str() != current && existing(key))
        .take(limit)
        .cloned()
        .collect()
}

/// One New Agent setup. `at` records a start but is not part of its identity.
#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct RecentSetup {
    pub machine: String,
    pub cwd: String,
    pub launcher: String,
    pub model: String,
    pub agent_type: String,
    pub mode: String,
    pub effort: String,
    pub at: String,
}

impl RecentSetup {
    pub fn same_setup(&self, other: &Self) -> bool {
        self.machine == other.machine
            && self.cwd == other.cwd
            && self.launcher == other.launcher
            && self.model == other.model
            && self.agent_type == other.agent_type
            && self.mode == other.mode
            && self.effort == other.effort
    }
}

/// Record a start: replace an identical setup's time and move it first, then retain the newest 20.
pub fn record_setup(before: &[RecentSetup], setup: &RecentSetup) -> Vec<RecentSetup> {
    std::iter::once(setup.clone())
        .chain(before.iter().filter(|entry| !entry.same_setup(setup)).cloned())
        .take(MAX_RECENT_SETUPS)
        .collect()
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq)]
pub struct Machine {
    pub id: String,
    pub online: bool,
}

/// A card points into the entries array so no setup data needs to be duplicated.
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize)]
pub struct SetupCard {
    pub index: usize,
    pub online: bool,
}

/// Keep entries for machines the hub still lists. Offline machines remain visible but not pickable.
/// If a machine occurs twice in the registry, its first record wins, matching the reference clients.
pub fn setup_cards(entries: &[RecentSetup], machines: &[Machine]) -> Vec<SetupCard> {
    let mut online = HashMap::new();
    for machine in machines {
        online.entry(machine.id.as_str()).or_insert(machine.online);
    }
    entries
        .iter()
        .enumerate()
        .filter_map(|(index, entry)| {
            online.get(entry.machine.as_str()).map(|&is_online| SetupCard {
                index,
                online: is_online,
            })
        })
        .take(MAX_RECENT_SETUP_CARDS)
        .collect()
}

/// Restore the model of the machine's newest entry with the selected launcher, or the launcher default (`""`).
pub fn setup_model<'a>(entries: &'a [RecentSetup], machine: &str, launcher: &str) -> &'a str {
    entries
        .iter()
        .find(|entry| entry.machine == machine && entry.launcher == launcher)
        .map_or("", |entry| entry.model.as_str())
}
