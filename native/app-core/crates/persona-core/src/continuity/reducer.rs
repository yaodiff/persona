//! One chat's items and how far they are known to be complete (`spec/fixtures/continuity/README.md`).
//!
//! The reference is iOS's `ChatStore`, not another client's older port. Repeated full items replace stale copies,
//! opened work stays expanded across repeated pages, and authoritative supervision evidence is retained monotonically.

use std::collections::HashSet;
use std::sync::Arc;

use crate::item::Item;

/// What a batch means for the chat's continuity.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Gap {
    /// Nothing to fetch.
    None,
    /// Lines are missing after `covered`: fetch them immediately.
    Fill,
    /// The chat cannot be stitched together: fetch the latest page instead.
    Reload,
}

/// The fields of one batch of items, as a response or a push carries them.
#[derive(Debug, Default)]
pub struct Batch {
    pub items: Vec<Item>,
    pub history: Option<String>,
    pub from_seq: Option<i64>,
    pub to_seq: Option<i64>,
    pub backlog: bool,
    pub has_more: Option<bool>,
    pub more_after: bool,
}

#[derive(Debug, Default)]
pub struct Reducer {
    items: Vec<Arc<Item>>,
    ids: HashSet<String>,
    history: Option<String>,
    covered: i64,
    has_more: Option<bool>,
    loaded_once: bool,
    /// Work opened by the user, retained when a repeated page still carries its fold.
    opened: std::collections::HashMap<String, Arc<Item>>,
}

impl Reducer {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn items(&self) -> &[Arc<Item>] {
        &self.items
    }
    pub fn history(&self) -> Option<&str> {
        self.history.as_deref()
    }
    pub fn covered(&self) -> i64 {
        self.covered
    }
    pub fn has_more(&self) -> Option<bool> {
        self.has_more
    }
    pub fn loaded_once(&self) -> bool {
        self.loaded_once
    }
    pub fn first_seq(&self) -> Option<i64> {
        self.items.first().map(|i| i.seq)
    }
    fn last_seq(&self) -> Option<i64> {
        self.items.last().map(|i| i.seq)
    }

    /// Restores cached state without treating the cache as a received backlog.
    pub fn restore(
        &mut self,
        items: Vec<Item>,
        history: Option<String>,
        covered: i64,
        has_more: Option<bool>,
        loaded_once: bool,
    ) {
        self.clear();
        self.opened.clear();
        self.items = items.into_iter().map(Arc::new).collect();
        self.history = history;
        self.covered = covered.max(0);
        self.has_more = has_more;
        self.loaded_once = loaded_once;
    }

    /// Applies a page or a push and says whether lines must be fetched.
    pub fn apply(&mut self, batch: Batch) -> Gap {
        let Batch {
            items,
            history,
            from_seq,
            to_seq,
            backlog,
            has_more,
            more_after,
        } = batch;
        if let (Some(new), Some(old)) = (&history, &self.history)
            && new != old
        {
            self.restore(Vec::new(), Some(new.clone()), 0, None, false);
            if from_seq.is_some_and(|f| f > 0) {
                return Gap::Reload;
            }
        }
        if history.is_some() {
            self.history = history.clone();
        }
        let Some(to_seq) = to_seq else {
            self.merge(items, backlog, has_more);
            self.covered = self.covered.max(self.last_seq().unwrap_or(0));
            return if more_after { Gap::Reload } else { Gap::None };
        };
        if let Some(from_seq) = from_seq {
            self.merge(items, backlog, has_more);
            if self.covered == 0 && from_seq > 0 {
                return if self.loaded_once { Gap::Reload } else { Gap::None };
            }
            if from_seq > self.covered {
                return Gap::Fill;
            }
            self.covered = self.covered.max(to_seq);
            return if more_after { Gap::Fill } else { Gap::None };
        }
        if self.covered > 0 && items.first().is_some_and(|first| first.seq > self.covered + 1) {
            self.replace(items, history, Some(to_seq), has_more.unwrap_or(true));
            if backlog {
                self.loaded_once = true;
            }
            return Gap::None;
        }
        self.merge(items, backlog, has_more);
        self.covered = self.covered.max(to_seq);
        Gap::None
    }

    /// A reload's latest page keeps strictly newer pushes of the same history, without claiming coverage through them.
    pub fn replace(&mut self, items: Vec<Item>, history: Option<String>, to_seq: Option<i64>, has_more: bool) {
        let same_history = history.is_none() || history == self.history;
        let keep: Vec<Arc<Item>> = match to_seq {
            Some(to) if same_history => self.items.iter().filter(|i| i.seq > to).cloned().collect(),
            _ => Vec::new(),
        };
        self.items.clear();
        self.ids.clear();
        self.opened.clear();
        self.has_more = Some(has_more);
        if history.is_some() {
            self.history = history;
        }
        self.merge(items, false, None);
        self.merge_shared(keep);
        self.covered = to_seq.unwrap_or_else(|| self.last_seq().unwrap_or(0));
    }

    /// An older page sets pagination directly, without changing history, coverage or `loaded_once`.
    pub fn older(&mut self, items: Vec<Item>, has_more: Option<bool>) {
        if has_more.is_some() {
            self.has_more = has_more;
        }
        self.merge(items, false, None);
    }

    /// Puts an opened fold's work back in its place, all pages at once, leaving out folds in the response.
    pub fn unfold(&mut self, fold_id: &str, work: Vec<Item>) {
        let Some(index) = self.items.iter().position(|i| i.id == fold_id) else {
            return;
        };
        let retained = self.items.remove(index);
        self.ids.remove(fold_id);
        self.opened.insert(fold_id.to_owned(), retained);
        self.merge(work.into_iter().filter(|i| i.kind != "fold").collect(), false, None);
    }

    pub(crate) fn find(&self, id: &str) -> Option<&Arc<Item>> {
        self.items.iter().find(|i| i.id == id)
    }

    fn clear(&mut self) {
        self.items.clear();
        self.ids.clear();
    }

    fn newest_copies(items: Vec<Arc<Item>>) -> Vec<Arc<Item>> {
        let mut seen = HashSet::new();
        let mut newest: Vec<Arc<Item>> = Vec::with_capacity(items.len());
        for item in items.into_iter().rev() {
            if seen.insert(item.id.clone()) {
                newest.push(item);
            }
        }
        newest.reverse();
        newest
    }

    /// iOS keeps authoritative supervision evidence when a newer copy, a replacement fold or its represented work
    /// arrives. The flag is synthesized on the retained item, not treated as a new transport field.
    fn retain_promotion(
        items: Vec<Arc<Item>>,
        opened: &std::collections::HashMap<String, Arc<Item>>,
        new: &[Arc<Item>],
        deduplicated: bool,
    ) -> Vec<Arc<Item>> {
        let evidence: Vec<&Item> = items
            .iter()
            .map(Arc::as_ref)
            .chain(opened.values().map(Arc::as_ref))
            .chain(new.iter().map(Arc::as_ref))
            .filter(|item| item.has_supervision_evidence())
            .collect();
        let promoted_ids: HashSet<String> = evidence
            .iter()
            .filter(|item| item.supervision_user)
            .map(|item| item.id.clone())
            .collect();
        let promoted_folds: Vec<Item> = evidence
            .iter()
            .filter(|item| item.fold.is_some())
            .map(|item| (*item).clone())
            .collect();
        let promoted_work: Vec<Item> = evidence
            .iter()
            .filter(|item| item.is_work())
            .map(|item| (*item).clone())
            .collect();
        let items = if deduplicated {
            Self::newest_copies(items)
        } else {
            items
        };
        items
            .into_iter()
            .map(|item| {
                if promoted_ids.contains(&item.id)
                    || item.is_work() && promoted_folds.iter().any(|fold| fold.folds(&item))
                    || item.fold.is_some() && promoted_work.iter().any(|work| item.folds(work))
                {
                    Arc::new((*item).clone().with_supervision())
                } else {
                    item
                }
            })
            .collect()
    }

    fn merge(&mut self, new: Vec<Item>, backlog: bool, has_more: Option<bool>) {
        let new_len = new.len();
        let new_min = new.iter().map(|i| i.seq).min();
        let deduplicated = self.items.len() != self.ids.len();
        let incoming = new.into_iter().map(Arc::new).collect::<Vec<_>>();
        self.items = Self::retain_promotion(std::mem::take(&mut self.items), &self.opened, &incoming, deduplicated);
        if deduplicated {
            self.items.sort_by_key(|item| item.key());
        }
        let incoming = Self::newest_copies(incoming);
        let promoted = Self::retain_promotion(incoming, &self.opened, &self.items, false);
        let batch_work: Vec<Arc<Item>> = promoted
            .iter()
            .filter(|i| i.is_work() && i.kind != "fold")
            .cloned()
            .collect();
        let fresh: Vec<Arc<Item>> = promoted
            .into_iter()
            .filter(|item| {
                let still_open = item.kind == "fold"
                    && self
                        .opened
                        .get(&item.id)
                        .is_some_and(|old| match (&old.fold, &item.fold) {
                            (Some(old), Some(now)) => {
                                (now.through_seq, crate::item::part(&now.through_id))
                                    <= (old.through_seq, crate::item::part(&old.through_id))
                            }
                            _ => false,
                        });
                !still_open && !(item.kind == "fold" && batch_work.iter().any(|work| item.folds(work)))
            })
            .collect();
        let folds: Vec<Arc<Item>> = fresh
            .iter()
            .filter(|item| item.kind == "fold" && !self.find(&item.id).is_some_and(|old| old.same_fold(item)))
            .cloned()
            .collect();
        self.items.retain(|old| {
            !(old.kind != "fold" && old.is_work() && folds.iter().any(|fold| fold.folds(old))
                || old.kind == "fold" && batch_work.iter().any(|work| old.folds(work)))
        });
        self.ids = self.items.iter().map(|i| i.id.clone()).collect();
        let existing_first = self.first_seq();
        for item in fresh {
            self.insert_shared(item);
        }
        let reaches_start = match (new_min, existing_first.or_else(|| self.items.first().map(|i| i.seq))) {
            (Some(min), Some(first)) => min <= first,
            _ => false,
        };
        if let Some(more) = has_more
            && backlog
            && (!self.loaded_once || self.items.len() <= new_len)
            && (more || reaches_start)
        {
            self.has_more = Some(more);
        }
        if backlog {
            self.loaded_once = true;
        }
    }

    fn merge_shared(&mut self, items: Vec<Arc<Item>>) {
        for item in items {
            self.insert_shared(item);
        }
    }

    fn insert_shared(&mut self, item: Arc<Item>) {
        // A repeated full item replaces the old copy, including changed receipt metadata. Keep its Arc when nothing
        // changed so the runtime's incremental diff stays cheap for append-only pushes.
        if let Some(old) = self.items.iter_mut().find(|old| old.id == item.id) {
            if old.json() != item.json() {
                *old = item;
            }
            return;
        }
        self.ids.insert(item.id.clone());
        let key = item.key();
        let index = if self.items.last().is_none_or(|last| last.key() <= key) {
            self.items.len()
        } else {
            self.items.partition_point(|i| i.key() <= key)
        };
        self.items.insert(index, item);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn duplicate_cache_keeps_promotion_on_the_newest_copy() {
        let mut reducer = Reducer::new();
        reducer.restore(
            Item::list_from_json(r#"[{"id":"1#0","seq":1,"kind":"text","text":"旧版","supervision_user":true},{"id":"1#0","seq":1,"kind":"text","text":"新版"}]"#).unwrap(),
            Some("h".into()),
            1,
            Some(false),
            false,
        );
        reducer.merge(Vec::new(), false, None);
        assert_eq!(reducer.items().len(), 1);
        assert_eq!(
            serde_json::from_str::<serde_json::Value>(reducer.items()[0].json()).unwrap(),
            serde_json::json!({"id":"1#0","seq":1,"kind":"text","text":"新版","supervision_user":true})
        );
    }

    #[test]
    fn partial_work_keeps_the_folds_promotion() {
        let mut reducer = Reducer::new();
        reducer.restore(
            Item::list_from_json(r#"[{"id":"fold:2#0","seq":2,"role":"assistant","kind":"fold","fold":{"through_seq":8,"through_id":"8#0","supervision_user":true}}]"#).unwrap(),
            Some("h".into()),
            8,
            Some(false),
            false,
        );
        let incoming =
            Item::list_from_json(r#"[{"id":"2#0","seq":2,"role":"assistant","kind":"thinking","text":"示例步骤"}]"#)
                .unwrap();
        assert!(reducer.items()[0].has_supervision_evidence());
        assert!(reducer.items()[0].folds(&incoming[0]));
        reducer.merge(incoming, false, None);
        assert_eq!(reducer.items().len(), 1);
        assert!(reducer.items()[0].supervision_user);
    }
}
