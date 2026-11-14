//! The Agents list's filters, order, groups and folds (`spec/fixtures/sort/`).
//!
//! This is a projection of values supplied by a client: it does not read preferences, a clock or a machine registry.
//! The rules follow `AgentListOrder`, `AgentListProjection`, `SpaceGroup` and `ChatsView` on iOS.

use std::cmp::Ordering;
use std::collections::{BTreeMap, BTreeSet, HashMap, HashSet};

use serde::{Deserialize, Serialize};

mod session;

pub use session::Session;

use crate::state::{AgentState, utc_timestamp};

/// The sessions, registry and preferences from which one Agents list is drawn.
#[derive(Clone, Debug, Deserialize)]
pub struct Input {
    pub sessions: Vec<Session>,
    pub machines: Vec<Machine>,
    pub pins: Pins,
    pub mutes: Mutes,
    pub settings: Settings,
}

#[derive(Clone, Debug, Deserialize)]
pub struct Machine {
    pub id: String,
    pub name: String,
    /// The hub group this machine belongs to, if not the default. `group|id` forms the full path,
    /// matching iOS's `MachineRef.path`.
    #[serde(default)]
    pub group: Option<String>,
}

impl Machine {
    /// The full path: `group|id` for a non-default hub group, bare `id` for the default.
    fn path(&self) -> String {
        match self.group.as_deref() {
            Some(group) if !group.is_empty() => format!("{group}|{}", self.id),
            _ => self.id.clone(),
        }
    }
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct Pins {
    #[serde(default)]
    pub spaces: Vec<String>,
    #[serde(default)]
    pub sessions: Vec<String>,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct Mutes {
    #[serde(default)]
    pub spaces: Vec<String>,
    #[serde(default)]
    pub sessions: Vec<String>,
    #[serde(default)]
    pub allowed: Vec<String>,
}

#[derive(Clone, Debug, Deserialize)]
pub struct Settings {
    pub grouping: Grouping,
    pub quick: Quick,
    /// `sort=updated` disables row rank, as on iOS; the default keeps the fixtures' state ordering.
    #[serde(default = "state_sort")]
    pub sort: String,
    #[serde(default)]
    pub query: String,
    #[serde(default)]
    pub machine_filter: String,
    #[serde(default)]
    pub kept: Vec<String>,
    #[serde(default)]
    pub folded: Vec<String>,
    #[serde(default)]
    pub expanded_idle: Vec<String>,
    #[serde(default)]
    pub expanded_quiet: Vec<String>,
    /// The row style does not affect the data projection.
    #[serde(default)]
    pub compact: bool,
    /// The open agent that was Unread when it was opened (`AppModel.heldInList`): it keeps its place and stays
    /// out of the idle folds until its chat is left (#2213).
    #[serde(default)]
    pub held: Option<String>,
    /// The caller's current time as an ISO 8601 string, used only by the `since:` / `after:` query token.
    /// The projection itself never reads a clock; without this field `since:` matches nothing.
    #[serde(default)]
    pub now: Option<String>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Grouping {
    State,
    Space,
    Machine,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Deserialize)]
pub enum Quick {
    #[serde(rename = "all")]
    All,
    #[serde(rename = "needsYou")]
    NeedsYou,
    #[serde(rename = "unread")]
    Unread,
    #[serde(rename = "working")]
    Working,
    #[serde(rename = "backgroundWaiting")]
    BackgroundWaiting,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct Projection {
    pub counts: Counts,
    pub groups: Vec<Group>,
    pub unread_shown: Vec<String>,
    /// By Project always carries this field; other groupings omit it.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub quiet_title: Option<Option<String>>,
}

fn state_sort() -> String {
    "state".to_owned()
}

#[derive(Clone, Debug, Default, Eq, PartialEq, Serialize)]
pub struct Counts {
    #[serde(rename = "needsYou")]
    pub needs_you: usize,
    pub unread: usize,
    pub working: usize,
    #[serde(rename = "backgroundWaiting")]
    pub background_waiting: usize,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct Group {
    pub id: String,
    pub title: String,
    pub rows: Vec<String>,
    pub visible_rows: Vec<String>,
    pub folded: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub parent: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub state_counts: Option<StateCounts>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub spoken: Option<String>,
}

#[derive(Clone, Debug, Default, Eq, PartialEq, Serialize)]
pub struct StateCounts {
    pub needs_you: usize,
    pub working: usize,
    pub unread: usize,
    pub idle: usize,
    pub unknown: usize,
}

#[derive(Clone, Debug)]
struct Row {
    id: String,
    short_id: String,
    session_id: String,
    /// `group|machine_id` for a non-default hub group, as iOS's `MachineRef.path`.
    machine_path: String,
    state: AgentState,
    sort_date: Option<i128>,
    is_waiting: bool,
    background_waiting: bool,
    pinned: bool,
    /// The open agent that was Unread when opened (`AppModel.heldInList`): keeps its rank-1 position,
    /// stays visible, and prevents its space from folding into quiet spaces (#2213).
    held: bool,
    muted: bool,
    awaits_reply: bool,
    space_id: String,
    space_name: String,
    slot_index: i64,
    slot_id: String,
}

impl Row {
    fn new(session: Session, input: &Input) -> Self {
        let state = AgentState::from_client(session.state.as_deref(), session.status.as_deref());
        let short_id = format!("{}/{}", session.machine_id, session.session_id);
        let pinned = input.pins.sessions.contains(&short_id);
        let muted = is_muted(&session, &input.mutes);
        let space_name = session.space_name.clone().unwrap_or_else(|| folder_name(&session.cwd));
        let group = session.app_hub_group.as_deref().unwrap_or("");
        let prefix = if group.is_empty() {
            String::new()
        } else {
            format!("{group}|")
        };
        let machine_path = format!("{prefix}{}", session.machine_id);
        let held = input.settings.held.as_deref() == Some(short_id.as_str());
        let sort_date = if state == AgentState::Working {
            session.state_since.as_deref().or(session.updated_at.as_deref())
        } else {
            session.updated_at.as_deref()
        };
        Self {
            id: format!("{prefix}{short_id}"),
            short_id,
            session_id: session.session_id.clone(),
            machine_path,
            state,
            sort_date: sort_date.and_then(utc_timestamp),
            is_waiting: state.is_quiet() && session.is_waiting(),
            background_waiting: state == AgentState::Working && session.background_waiting,
            pinned,
            held,
            muted,
            awaits_reply: session.awaits_reply,
            space_id: session.space_id.clone().unwrap_or_else(|| "-".to_owned()),
            space_name,
            slot_index: session.slot_index.unwrap_or(0),
            slot_id: session.slot_id.clone().unwrap_or_default(),
        }
    }

    fn stays(&self) -> bool {
        !self.state.is_quiet() || self.pinned || self.held || self.is_waiting || self.awaits_reply()
    }

    fn awaits_reply(&self) -> bool {
        self.state == AgentState::Idle && self.awaits_reply && !self.muted
    }

    fn counted_state(&self) -> AgentState {
        if self.background_waiting {
            AgentState::Idle
        } else {
            self.state
        }
    }

    /// The index into the eight By Status sections. Needs You never drops because of a mute.
    /// A held agent that has become quiet keeps its Unread rank, as iOS's `rank` does (#2213).
    fn rank(&self) -> usize {
        if self.state == AgentState::NeedsYou {
            return 0;
        }
        let unread = self.state == AgentState::Unread || (self.held && self.state.is_quiet());
        let live = self.state == AgentState::Working || unread || self.is_waiting;
        if live && self.muted {
            return 6;
        }
        if unread {
            return 1;
        }
        if self.awaits_reply() {
            return 2;
        }
        match self.state {
            AgentState::Working if self.background_waiting => 4,
            AgentState::Working => 3,
            _ if self.is_waiting => 5,
            _ => 7,
        }
    }
}

#[derive(Default)]
struct Query {
    words: Vec<String>,
    machines: Vec<String>,
    projects: Vec<String>,
    harnesses: Vec<String>,
    models: Vec<String>,
    states: Vec<String>,
    pinned_only: bool,
    errors_only: bool,
    /// Parsed `since:` window in hours, matching iOS's `AgentParsedQuery.sinceHours`.
    since_hours: Option<f64>,
}

impl Query {
    fn parse(raw: &str, machine_filter: &str) -> Self {
        let mut query = Self::default();
        if !machine_filter.is_empty() {
            query.machines.push(machine_filter.to_lowercase());
        }
        for token in raw.split_whitespace() {
            let Some((key, value)) = token.split_once(':') else {
                query.words.push(token.to_lowercase());
                continue;
            };
            if key.is_empty() || value.is_empty() {
                query.words.push(token.to_lowercase());
                continue;
            }
            let value = value.to_lowercase();
            match key.to_lowercase().as_str() {
                "machine" | "host" => query.machines.push(value),
                "project" | "repo" | "cwd" | "path" => query.projects.push(value),
                "agent" | "harness" | "launcher" => query.harnesses.push(value),
                "model" => query.models.push(value),
                "state" | "status" => query.states.push(value),
                "is" if matches!(value.as_str(), "pinned" | "pin") => query.pinned_only = true,
                "is" if matches!(value.as_str(), "error" | "errors" | "failed" | "blocked") => {
                    query.errors_only = true;
                }
                "is" => query.states.push(value),
                "since" | "after" | "updated"
                    if matches!(value.as_str(), "24h" | "1d" | "48h" | "2d" | "7d" | "1w" | "30d" | "1m") =>
                {
                    let hours: f64 = match value.as_str() {
                        "24h" | "1d" | "48h" | "2d" => 24.0,
                        "7d" | "1w" => 168.0,
                        "30d" | "1m" => 720.0,
                        _ => unreachable!(),
                    };
                    query.since_hours = Some(query.since_hours.map_or(hours, |prev: f64| prev.min(hours)));
                }
                _ => query.words.push(token.to_lowercase()),
            }
        }
        query
    }

    fn matches(&self, session: &Session, input: &Input) -> bool {
        if session.assistant {
            return false;
        }
        let machine = input.machines.iter().find(|machine| {
            machine.id == session.machine_id
                && machine.group.as_deref().unwrap_or("") == session.app_hub_group.as_deref().unwrap_or("")
        });
        let machine_name = machine.map_or("", |machine| machine.name.as_str()).to_lowercase();
        let machine_path = {
            let group = session.app_hub_group.as_deref().unwrap_or("");
            if group.is_empty() {
                session.machine_id.to_lowercase()
            } else {
                format!("{group}|{}", session.machine_id).to_lowercase()
            }
        };
        if !self
            .machines
            .iter()
            .all(|word| machine_path.contains(word) || machine_name.contains(word))
        {
            return false;
        }
        let short_id = format!("{}/{}", session.machine_id, session.session_id);
        if self.pinned_only && !input.pins.sessions.contains(&short_id) {
            return false;
        }
        if self.errors_only && !has_error(session) {
            return false;
        }
        let project = session
            .repo_root
            .as_deref()
            .filter(|root| !root.is_empty())
            .map(folder_name)
            .or_else(|| session.space_name.clone().filter(|name| !name.trim().is_empty()))
            .unwrap_or_else(|| folder_name(&session.cwd))
            .to_lowercase();
        let cwd = session.cwd.to_lowercase();
        if !self
            .projects
            .iter()
            .all(|word| project.contains(word) || cwd.contains(word))
        {
            return false;
        }
        let harness = session.harness_id().to_lowercase();
        let agent_label = session.agent_label().to_lowercase();
        if !self
            .harnesses
            .iter()
            .all(|word| harness.contains(word) || agent_label.contains(word))
        {
            return false;
        }
        let model = format!(
            "{} {}",
            session.model.as_deref().unwrap_or(""),
            session.launcher.as_deref().unwrap_or("")
        )
        .to_lowercase();
        if !self.models.iter().all(|word| model.contains(word)) {
            return false;
        }
        let state = AgentState::from_client(session.state.as_deref(), session.status.as_deref());
        if !self.states.is_empty() && !self.states.contains(&state.as_str().to_owned()) {
            return false;
        }
        if let Some(hours) = self.since_hours {
            // iOS: `guard let updated = session.updatedAt, Date.now.timeIntervalSince(updated) <= hrs * 3600 else { return false }`.
            // The caller supplies `now`; the projection never reads a clock.
            let Some(now) = input.settings.now.as_deref().and_then(utc_timestamp) else {
                return false;
            };
            let Some(updated) = session.updated_at.as_deref().and_then(utc_timestamp) else {
                return false;
            };
            let elapsed_seconds = now.saturating_sub(updated) as f64 / 1_000_000_000.0;
            if elapsed_seconds > hours * 3600.0 {
                return false;
            }
        }
        let agent_label = session.agent_label();
        let fields = [
            session.title.as_str(),
            session.cwd.as_str(),
            session.repo_root.as_deref().unwrap_or(""),
            session.branch.as_deref().unwrap_or(""),
            session.last_text.as_deref().unwrap_or(""),
            session.asks.as_deref().unwrap_or(""),
            session.space_name.as_deref().unwrap_or(""),
            session.slot_name.as_deref().unwrap_or(""),
            agent_label.as_str(),
            session.model.as_deref().unwrap_or(""),
            session.launcher.as_deref().unwrap_or(""),
            session.start_error.as_deref().unwrap_or(""),
            session.turn_error.as_deref().unwrap_or(""),
        ];
        self.words
            .iter()
            .all(|word| fields.iter().any(|field| field.to_lowercase().contains(word)))
    }
}

fn has_error(session: &Session) -> bool {
    [
        session.start_error.as_deref(),
        session.turn_error.as_deref(),
        session.failure_reason.as_deref(),
        session.attention_reason.as_deref(),
    ]
    .into_iter()
    .flatten()
    .any(|text| !text.is_empty())
        || session.work.as_ref().is_some_and(|work| work.phase == "blocked")
        || AgentState::from_client(session.state.as_deref(), session.status.as_deref()) == AgentState::NeedsYou
}

/// Build one immutable Agents list. The expected fixture objects are compared as complete JSON values by its test.
pub fn project(input: &Input) -> Projection {
    project_impl(input, false)
}

fn project_impl(input: &Input, oldest_first: bool) -> Projection {
    let query = Query::parse(&input.settings.query, &input.settings.machine_filter);
    let base: Vec<Row> = input
        .sessions
        .iter()
        .filter(|session| query.matches(session, input))
        .cloned()
        .map(|session| Row::new(session, input))
        .collect();
    let counts = counts(&base);
    let quick = input.settings.quick;
    let matching: HashSet<&str> = if quick == Quick::All {
        HashSet::new()
    } else {
        base.iter()
            .filter(|row| quick_matches(row, quick))
            .map(|row| row.id.as_str())
            .collect()
    };
    let kept: HashSet<&str> = input.settings.kept.iter().map(String::as_str).collect();
    let filtered: Vec<&Row> = base
        .iter()
        .filter(|row| quick == Quick::All || matching.contains(row.id.as_str()) || kept.contains(row.id.as_str()))
        .collect();
    let unread_shown = filtered
        .iter()
        .filter(|row| row.state == AgentState::Unread)
        .map(|row| row.id.clone())
        .collect();
    let mut groups = Vec::new();
    let mut quiet_title = None;
    let pinned = pinned_sessions(&filtered, input);
    if !pinned.is_empty() {
        groups.push(Group {
            id: "pinned".to_owned(),
            title: "置顶".to_owned(), // i18n-ignore
            rows: pinned.iter().map(|row| row.id.clone()).collect(),
            visible_rows: pinned.iter().map(|row| row.id.clone()).collect(),
            folded: false,
            parent: None,
            state_counts: None,
            spoken: None,
        });
    }
    let unpinned: Vec<&Row> = filtered.into_iter().filter(|row| !row.pinned).collect();
    match input.settings.grouping {
        Grouping::State => groups.extend(state_groups(
            &unpinned,
            quick,
            &input.settings,
            input.settings.sort != "updated",
            oldest_first,
        )),
        Grouping::Machine => groups.extend(machine_groups(
            &unpinned,
            input,
            quick,
            input.settings.sort != "updated",
        )),
        Grouping::Space => {
            let (mut spaces, quiet) = space_groups(&unpinned, input, quick, input.settings.sort == "updated");
            groups.append(&mut spaces);
            if quiet {
                quiet_title = Some("空闲的空间".to_owned()); // i18n-ignore
            }
        }
    }
    Projection {
        counts,
        groups,
        unread_shown,
        quiet_title: (input.settings.grouping == Grouping::Space).then_some(quiet_title),
    }
}

/// Mutation-probe entry point used by the integration test, not by the normal projection.
#[doc(hidden)]
pub fn project_with_order(input: &Input) -> Projection {
    project_impl(input, true)
}

fn counts(base: &[Row]) -> Counts {
    let mut counts = Counts::default();
    for row in base {
        match row.counted_state() {
            // ChatsView.count(_:) counts by countedState without excluding mutes (the filter bar's
            // `filterCounts` is a separate predicate, AgentFilter.matches, which does exclude them).
            AgentState::NeedsYou => counts.needs_you += 1,
            AgentState::Unread => counts.unread += 1,
            AgentState::Working => counts.working += 1,
            _ => {}
        }
        if row.background_waiting && !row.muted {
            counts.background_waiting += 1;
        }
    }
    counts
}

fn quick_matches(row: &Row, quick: Quick) -> bool {
    match quick {
        Quick::All => true,
        Quick::NeedsYou => row.state == AgentState::NeedsYou,
        Quick::Unread => row.counted_state() == AgentState::Unread && !row.muted,
        Quick::Working => row.counted_state() == AgentState::Working && !row.muted,
        Quick::BackgroundWaiting => row.background_waiting && !row.muted,
    }
}

fn ordered<'a>(rows: impl IntoIterator<Item = &'a Row>, by_rank: bool) -> Vec<&'a Row> {
    ordered_with(rows, by_rank, false)
}

/// `oldest_first` exists only to exercise a wrong comparison in the fixture mutation test.
fn ordered_with<'a>(rows: impl IntoIterator<Item = &'a Row>, by_rank: bool, oldest_first: bool) -> Vec<&'a Row> {
    let mut rows: Vec<&Row> = rows.into_iter().collect();
    rows.sort_by(|a, b| {
        let rank = if by_rank {
            a.rank().cmp(&b.rank())
        } else {
            Ordering::Equal
        };
        let dates = if oldest_first {
            a.sort_date.cmp(&b.sort_date)
        } else {
            b.sort_date.cmp(&a.sort_date)
        };
        rank.then_with(|| dates)
            .then_with(|| a.session_id.cmp(&b.session_id))
            .then_with(|| a.id.cmp(&b.id))
    });
    rows
}

fn pinned_sessions<'a>(rows: &[&'a Row], input: &Input) -> Vec<&'a Row> {
    let positions: HashMap<&str, usize> = input
        .pins
        .sessions
        .iter()
        .enumerate()
        .map(|(index, id)| (id.as_str(), index))
        .collect();
    let mut pinned: Vec<&Row> = rows.iter().copied().filter(|row| row.pinned).collect();
    pinned.sort_by_key(|row| positions.get(row.short_id.as_str()).copied().unwrap_or(0));
    pinned
}

const STATE_SECTIONS: [(&str, &str); 8] = [
    ("needs", "需要你"),          // i18n-ignore
    ("unread", "完成未看"),       // i18n-ignore
    ("awaiting", "待你回复"),     // i18n-ignore
    ("working", "工作中"),        // i18n-ignore
    ("background", "后台等待中"), // i18n-ignore
    ("waiting", "待唤醒"),        // i18n-ignore
    ("muted", "已屏蔽"),          // i18n-ignore
    ("idle", "已看"),             // i18n-ignore
];

fn state_groups(rows: &[&Row], quick: Quick, settings: &Settings, by_rank: bool, oldest_first: bool) -> Vec<Group> {
    let live: Vec<&Row> = rows.iter().copied().filter(|row| row.state.is_live()).collect();
    STATE_SECTIONS
        .iter()
        .enumerate()
        .filter(|(rank, _)| {
            // Show every section that has rows, including kept rows that a quick filter retained.
            // iOS's stateSections shows every nonempty section; the quick filter narrows `filtered`,
            // not which sections appear.
            live.iter().any(|row| row.rank() == *rank)
        })
        .map(|(rank, (key, title))| {
            let id = format!("state/{key}");
            let members = ordered_with(
                live.iter().copied().filter(|row| row.rank() == rank),
                quick == Quick::All && by_rank,
                oldest_first,
            );
            let folded = is_folded(&id, settings);
            plain_group(id, (*title).to_owned(), members, folded, quick, settings, None)
        })
        .collect()
}

fn machine_groups(rows: &[&Row], input: &Input, quick: Quick, by_rank: bool) -> Vec<Group> {
    let recent_sort = input.settings.sort == "updated";
    let mut machines_with_rows: Vec<(&Machine, Vec<&Row>)> = input
        .machines
        .iter()
        .filter_map(|machine| {
            let path = machine.path();
            let members: Vec<&Row> = rows
                .iter()
                .copied()
                .filter(|row| row.machine_path == path && row.state.is_live())
                .collect();
            if members.is_empty() {
                return None;
            }
            Some((machine, members))
        })
        .collect();
    // Under Recently Updated, machines sort by their newest session's sortDate, then by path.
    if recent_sort {
        machines_with_rows.sort_by(|(a, a_rows), (b, b_rows)| {
            let a_date = a_rows.iter().filter_map(|r| r.sort_date).max();
            let b_date = b_rows.iter().filter_map(|r| r.sort_date).max();
            b_date.cmp(&a_date).then_with(|| a.path().cmp(&b.path()))
        });
    }
    machines_with_rows
        .into_iter()
        .map(|(machine, members)| {
            let path = machine.path();
            let members = ordered(members, quick == Quick::All && by_rank);
            let id = format!("machine/{path}");
            plain_group(
                id,
                machine.name.clone(),
                members,
                is_folded(&format!("machine/{path}"), &input.settings),
                quick,
                &input.settings,
                Some(format!("machine-{path}")),
            )
        })
        .collect()
}

fn plain_group(
    id: String,
    title: String,
    members: Vec<&Row>,
    folded: bool,
    quick: Quick,
    settings: &Settings,
    idle_key: Option<String>,
) -> Group {
    let visible_rows = if folded {
        Vec::new()
    } else if quick != Quick::All {
        ids(&members)
    } else {
        let expand = idle_key
            .as_ref()
            .is_some_and(|key| settings.expanded_idle.contains(key))
            || !settings.query.is_empty();
        members
            .iter()
            .filter(|row| row.stays() || expand || idle_key.is_none())
            .map(|row| row.id.clone())
            .collect()
    };
    Group {
        id,
        title,
        rows: ids(&members),
        visible_rows,
        folded,
        parent: None,
        state_counts: None,
        spoken: None,
    }
}

struct Space<'a> {
    id: String,
    name: String,
    members: Vec<&'a Row>,
    priority: u8,
    sort_date: Option<i128>,
    quiet: bool,
    pinned: bool,
    muted: bool,
}

fn space_groups(rows: &[&Row], input: &Input, quick: Quick, recent_sort: bool) -> (Vec<Group>, bool) {
    let mut spaces: BTreeMap<String, Space> = BTreeMap::new();
    for row in rows.iter().copied().filter(|row| row.state.is_live()) {
        let space = spaces
            .entry(format!("{}/{}", row.machine_path, row.space_id))
            .or_insert_with(|| Space {
                id: format!("{}/{}", row.machine_path, row.space_id),
                name: row.space_name.clone(),
                members: Vec::new(),
                priority: u8::MAX,
                sort_date: None,
                quiet: true,
                pinned: false,
                muted: false,
            });
        space.priority = space.priority.min(priority(row.counted_state()));
        space.sort_date = space.sort_date.max(row.sort_date);
        space.quiet &= row.state.is_quiet() && !row.is_waiting && !row.awaits_reply() && !row.held;
        space.members.push(row);
    }
    let mut spaces: Vec<Space> = spaces.into_values().collect();
    for space in &mut spaces {
        let key = format!(
            "{}/{}",
            space.id.split_once('/').map_or("", |(machine, _)| machine),
            space.name
        );
        space.pinned = input.pins.spaces.contains(&key);
        space.muted = input.mutes.spaces.contains(&key);
        space.members.sort_by(|a, b| {
            a.slot_index
                .cmp(&b.slot_index)
                .then_with(|| a.slot_id.cmp(&b.slot_id))
                .then_with(|| a.session_id.cmp(&b.session_id))
                .then_with(|| a.id.cmp(&b.id))
        });
    }
    spaces.sort_by(|a, b| {
        if !recent_sort {
            a.priority.cmp(&b.priority)
        } else {
            Ordering::Equal
        }
        .then_with(|| b.sort_date.cmp(&a.sort_date))
        .then_with(|| a.id.cmp(&b.id))
    });
    fn by_time(mut items: Vec<Space<'_>>) -> Vec<Space<'_>> {
        items.sort_by(|a: &Space, b: &Space| b.sort_date.cmp(&a.sort_date).then_with(|| a.id.cmp(&b.id)));
        items
    }
    let pinned = |space: &&Space| space.pinned && !space.muted;
    let (active, quiet): (Vec<Space>, Vec<Space>) = if quick == Quick::All && !recent_sort {
        let mut pinned_spaces: Vec<Space> = Vec::new();
        let mut busy = Vec::new();
        let mut muted = Vec::new();
        let mut quiet = Vec::new();
        for space in spaces {
            if pinned(&&space) {
                pinned_spaces.push(space);
            } else if space.quiet {
                quiet.push(space);
            } else if space.muted {
                muted.push(space);
            } else {
                busy.push(space);
            }
        }
        pinned_spaces.extend(busy);
        pinned_spaces.extend(muted);
        (pinned_spaces, quiet)
    } else {
        let mut active = Vec::new();
        let mut rest = Vec::new();
        for space in spaces {
            if pinned(&&space) {
                active.push(space);
            } else {
                rest.push(space);
            }
        }
        active.extend(by_time(rest));
        (active, Vec::new())
    };
    let mut groups: Vec<Group> = active
        .into_iter()
        .map(|space| make_space(space, false, input, quick, recent_sort))
        .collect();
    let has_quiet = !quiet.is_empty();
    groups.extend(
        quiet
            .into_iter()
            .map(|space| make_space(space, true, input, quick, recent_sort)),
    );
    (groups, has_quiet)
}

fn make_space(space: Space, quiet: bool, input: &Input, quick: Quick, by_time: bool) -> Group {
    let folded = if quiet {
        !input.settings.expanded_quiet.contains(&space.id) && input.settings.query.is_empty()
    } else {
        is_folded(&space.id, &input.settings)
    };
    let visible_rows = if folded {
        Vec::new()
    } else if quiet {
        ids(&space.members)
    } else {
        let members = ordered(space.members.iter().copied(), quick == Quick::All && !by_time);
        let expand = input.settings.expanded_idle.contains(&space.id) || !input.settings.query.is_empty();
        members
            .iter()
            .filter(|row| quick != Quick::All || row.stays() || expand)
            .map(|row| row.id.clone())
            .collect()
    };
    let state_counts = state_counts(&space.members);
    let rows = if quiet {
        ids(&space.members)
    } else {
        ids(&ordered(space.members.iter().copied(), quick == Quick::All && !by_time))
    };
    Group {
        id: space.id,
        title: space.name,
        rows,
        visible_rows,
        folded,
        parent: quiet.then(|| "quiet".to_owned()),
        spoken: Some(spoken(
            &state_counts,
            space.members.iter().filter(|row| row.background_waiting).count(),
        )),
        state_counts: Some(state_counts),
    }
}

fn state_counts(rows: &[&Row]) -> StateCounts {
    let mut counts = StateCounts::default();
    for row in rows {
        match row.counted_state() {
            AgentState::NeedsYou => counts.needs_you += 1,
            AgentState::Working => counts.working += 1,
            AgentState::Unread => counts.unread += 1,
            AgentState::Idle => counts.idle += 1,
            AgentState::Unknown => counts.unknown += 1,
            AgentState::Offline => {}
        }
    }
    counts
}

fn spoken(counts: &StateCounts, background_waiting: usize) -> String {
    let mut phrases = Vec::new();
    for (count, label) in [
        (counts.needs_you, "需要你"),                                // i18n-ignore
        (counts.working, "工作中"),                                  // i18n-ignore
        (counts.unread, "完成未看"),                                 // i18n-ignore
        (background_waiting, "后台等待中"),                          // i18n-ignore
        (counts.idle + counts.unknown - background_waiting, "空闲"), // i18n-ignore
    ] {
        if count > 0 {
            phrases.push(format!("{count} 个{label}")); // i18n-ignore
        }
    }
    phrases.join("，") // i18n-ignore
}

fn priority(state: AgentState) -> u8 {
    match state {
        AgentState::NeedsYou => 0,
        AgentState::Unread => 1,
        AgentState::Working => 2,
        AgentState::Idle => 3,
        AgentState::Unknown => 4,
        AgentState::Offline => 5,
    }
}

fn is_folded(id: &str, settings: &Settings) -> bool {
    if !settings.query.is_empty() {
        return false;
    }
    let folded: BTreeSet<&str> = settings.folded.iter().map(String::as_str).collect();
    if id == "state/idle" {
        !folded.contains("!state/idle")
    } else {
        folded.contains(id)
    }
}

fn ids(rows: &[&Row]) -> Vec<String> {
    rows.iter().map(|row| row.id.clone()).collect()
}

fn is_muted(session: &Session, mutes: &Mutes) -> bool {
    let id = format!("{}/{}", session.machine_id, session.session_id);
    if mutes.sessions.contains(&id) {
        return true;
    }
    if mutes.allowed.contains(&id) {
        return false;
    }
    session
        .space_name
        .as_ref()
        .is_some_and(|name| mutes.spaces.contains(&format!("{}/{name}", session.machine_id)))
}

fn folder_name(path: &str) -> String {
    path.trim_end_matches('/').rsplit('/').next().unwrap_or(path).to_owned()
}
