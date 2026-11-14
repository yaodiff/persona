//! When a chat asks the hub for lines: immediate catch-up with an in-flight gate, at most eight pages before the
//! latest page instead, reconnect subscriptions, older history and an opened fold's pages. No clocks or transport:
//! the caller runs the requests and reports their results as events.

use serde::{Deserialize, Serialize};

use super::reducer::{Batch, Gap, Reducer};
use crate::item::Item;

/// Catch-up pages before giving up and fetching the latest page.
pub const MAX_FILL_PAGES: u32 = 8;
/// Pages of one opened fold before publishing what arrived.
pub const MAX_RANGE_PAGES: u32 = 100;

/// A request the chat makes.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum Request {
    FetchAfter {
        after_seq: i64,
    },
    FetchLatest {
        fold_work: bool,
    },
    /// `since_seq` only once there is a baseline.
    Subscribe {
        since_seq: Option<i64>,
        fold_work: bool,
    },
    FetchOlder {
        before_seq: i64,
        fold_work: bool,
    },
    /// Both ends included; the next page starts after the last item received. `fold_id` names which opening the
    /// page belongs to, so independent folds can be in flight at once; it is `None` for the older single-fold
    /// callers, which keep their behaviour.
    FetchRange {
        #[serde(default)]
        fold_id: Option<String>,
        from_seq: i64,
        through_seq: i64,
    },
}

/// One chat: its items and the requests in flight.
#[derive(Debug, Default)]
pub struct Session {
    reducer: Reducer,
    filling: bool,
    fill_pages: u32,
    /// Gaps and rev moves heard, and how many of them the newest read covers: a read covers what was heard when it
    /// was sent, not when it came back. A move heard while a read is on its way is read again once that read ends.
    heard: u64,
    asked: u64,
    loading_older: bool,
    /// Folds opened by the user, by fold id: iOS opens different folds independently and completes them in either
    /// order (docs/plans/plan-rust-core.md, decision 7), so one opening cannot cancel another.
    openings: std::collections::BTreeMap<String, Opening>,
    /// Whether the chat's items are known complete: the last batch left no gap, and it was a backlog (a page counted
    /// from the end) or the chat was already verified. This is the rule Android derives per client today (#7448);
    /// the core owns it so no client re-implements the reducer's gap logic.
    verified: bool,
}

/// One fold being opened: how far its range reaches, the pages so far and the work they carried.
#[derive(Debug, Default)]
struct Opening {
    through: i64,
    pages: u32,
    work: Vec<Item>,
}

/// The result of one event: the gap a batch left and the requests it starts.
#[derive(Debug, Default, PartialEq, Eq)]
pub struct Step {
    pub gap: Option<Gap>,
    pub requests: Vec<Request>,
}

impl Session {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn reducer(&self) -> &Reducer {
        &self.reducer
    }

    pub fn restore(
        &mut self,
        items: Vec<Item>,
        history: Option<String>,
        covered: i64,
        has_more: Option<bool>,
        loaded_once: bool,
    ) {
        self.reducer.restore(items, history, covered, has_more, loaded_once);
        self.verified = false;
    }

    /// The newest read may miss a line.
    pub fn behind(&self) -> bool {
        self.heard > self.asked
    }

    /// The latest page or a push.
    pub fn received(&mut self, batch: Batch) -> Step {
        let backlog = batch.backlog;
        let gap = self.reducer.apply(batch);
        self.verified = gap == Gap::None && (backlog || self.verified);
        Step {
            gap: Some(gap),
            requests: self.settle(gap),
        }
    }

    /// Whether the chat's items are known complete, as Android's `verified` means it: the last batch left no gap,
    /// and it was a backlog or the chat was already verified.
    pub fn verified(&self) -> bool {
        self.verified
    }

    /// iOS starts a fill as soon as it finds a gap. An already-running fill is not duplicated: its pages re-read
    /// `covered`, and `heard` / `asked` make a move heard in flight get another page when this one ends.
    fn settle(&mut self, gap: Gap) -> Vec<Request> {
        match gap {
            Gap::None => Vec::new(),
            Gap::Fill => {
                self.heard += 1;
                self.continue_fill().requests
            }
            Gap::Reload => vec![Request::FetchLatest { fold_work: true }],
        }
    }

    /// The hub signalled that this chat changed (a rev move, a wake): settle as if a gap were found, recording
    /// that a change was heard so a read in flight is followed by another. This is iOS's `settle(.fill)`
    /// (`AppModel.settle`), which the wire previously could not express (#7448).
    pub fn hub_changed(&mut self) -> Step {
        Step {
            gap: Some(Gap::Fill),
            requests: self.settle(Gap::Fill),
        }
    }

    /// Continues an existing fill, or starts one now from the current `covered`.
    pub fn continue_fill(&mut self) -> Step {
        if self.filling {
            return Step::default();
        }
        self.filling = true;
        self.fill_pages = 0;
        self.asked = self.heard;
        Step {
            gap: None,
            requests: vec![Request::FetchAfter {
                after_seq: self.reducer.covered(),
            }],
        }
    }

    /// A catch-up page. It never carries older-history pagination.
    pub fn after(&mut self, mut batch: Batch) -> Step {
        batch.backlog = false;
        batch.has_more = None;
        let covered_before = self.reducer.covered();
        let to_seq = batch.to_seq;
        let gap = self.reducer.apply(batch);
        // Android's catch-up rule (`HubChatRepository.catchUp`): a complete answer with nothing past the covered
        // point verifies the chat again, so a recovered fill restores what a gap cleared. An item beyond `covered`
        // means a live push overtook the page, which is not yet whole.
        self.verified = gap == Gap::None && !self.overtaken();
        self.fill_pages += 1;
        let progressed = self.reducer.covered() > covered_before || to_seq.is_some_and(|to| to > covered_before);
        // A late capped page whose range is already covered must not request the same position again.
        let again = gap == Gap::None && self.behind();
        let requests = if gap == Gap::Reload || (gap == Gap::Fill || again) && self.fill_pages >= MAX_FILL_PAGES {
            vec![Request::FetchLatest { fold_work: true }]
        } else if (gap == Gap::Fill && progressed) || again {
            vec![Request::FetchAfter {
                after_seq: self.reducer.covered(),
            }]
        } else {
            self.finish_fill();
            Vec::new()
        };
        if !requests.is_empty() {
            self.asked = self.heard;
        }
        Step {
            gap: Some(gap),
            requests,
        }
    }

    /// Also called when a catch-up or reload fails or is cancelled, to let the next one start.
    pub fn finish_fill(&mut self) {
        self.filling = false;
        self.fill_pages = 0;
    }

    /// The latest page fetched instead of filling.
    pub fn reload(&mut self, items: Vec<Item>, history: Option<String>, to_seq: Option<i64>, has_more: bool) -> Step {
        self.reducer.replace(items, history, to_seq, has_more);
        self.finish_fill();
        // A reload that keeps later pushes is not yet whole: Android uses `!overtaken` and asks for another fill
        // (`HubChatRepository.reload`), rather than marking the chat verified.
        self.verified = !self.overtaken();
        Step::default()
    }

    /// Any item past the covered point: a live push overtook the page that was just applied.
    fn overtaken(&self) -> bool {
        self.reducer
            .items()
            .iter()
            .any(|item| item.seq > self.reducer.covered())
    }

    pub fn reconnect(&mut self) -> Step {
        let covered = self.reducer.covered();
        Step {
            gap: None,
            requests: vec![Request::Subscribe {
                since_seq: (covered > 0).then_some(covered),
                fold_work: true,
            }],
        }
    }

    pub fn load_older(&mut self) -> Step {
        if self.loading_older || self.reducer.has_more() != Some(true) {
            return Step::default();
        }
        let Some(first) = self.reducer.first_seq() else {
            return Step::default();
        };
        self.loading_older = true;
        Step {
            gap: None,
            requests: vec![Request::FetchOlder {
                before_seq: first,
                fold_work: true,
            }],
        }
    }

    pub fn older(&mut self, items: Vec<Item>, has_more: bool) -> Step {
        self.reducer.older(items, Some(has_more));
        self.finish_older();
        Step::default()
    }

    /// Also called when an older page fails or is cancelled.
    pub fn finish_older(&mut self) {
        self.loading_older = false;
    }

    /// Opens a fold. Different folds are independent, as on iOS: opening one does not cancel another, and they
    /// complete in either order. Opening the same fold again while it is in flight asks for nothing new.
    pub fn open_fold(&mut self, id: &str) -> Step {
        if self.openings.contains_key(id) {
            return Step::default();
        }
        let Some(item) = self.reducer.find(id).filter(|i| i.kind == "fold") else {
            return Step::default();
        };
        let Some(range) = &item.fold else {
            return Step::default();
        };
        let (seq, through) = (item.seq, range.through_seq);
        self.openings.insert(
            id.to_owned(),
            Opening {
                through,
                pages: 0,
                work: Vec::new(),
            },
        );
        Step {
            gap: None,
            requests: vec![Request::FetchRange {
                fold_id: Some(id.to_owned()),
                from_seq: seq,
                through_seq: through,
            }],
        }
    }

    /// A page of an opened fold, named by its fold id. The work is published once, when the last page arrives.
    /// A page for a fold that is not opening is ignored, as before.
    pub fn range(&mut self, fold_id: &str, items: Vec<Item>, more: bool) -> Step {
        // A page without a fold id answers the only opening, which is what the older single-fold callers meant.
        let id = if fold_id.is_empty() {
            match self.openings.len() {
                1 => self.openings.keys().next().expect("one opening").clone(),
                _ => return Step::default(),
            }
        } else {
            fold_id.to_owned()
        };
        let Some(opening) = self.openings.get_mut(&id) else {
            return Step::default();
        };
        let last = items.last().map(|i| i.seq);
        opening.work.extend(items);
        opening.pages += 1;
        let through = opening.through;
        if more
            && let Some(last) = last
            && opening.pages < MAX_RANGE_PAGES
        {
            return Step {
                gap: None,
                requests: vec![Request::FetchRange {
                    fold_id: Some(id.clone()),
                    from_seq: last + 1,
                    through_seq: through,
                }],
            };
        }
        let opening = self.openings.remove(&id).expect("the opening was present");
        self.reducer.unfold(&id, opening.work);
        Step::default()
    }

    /// Drops unpublished fold pages after a failure or cancellation. With no id, drops every opening, as when the
    /// chat is closed.
    pub fn cancel_fold(&mut self, fold_id: Option<&str>) {
        match fold_id {
            Some(id) => {
                self.openings.remove(id);
            }
            None => self.openings.clear(),
        }
    }
}
