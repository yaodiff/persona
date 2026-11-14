//! Unified Agent states, decoded as the iOS client and the hub do
//! (`spec/fixtures/sort/session_state.json`, `hub_state.json`).

use serde::{Deserialize, Serialize};

/// A state known to every client. A future wire value decodes as `Unknown`.
#[derive(Clone, Copy, Debug, Default, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum AgentState {
    #[serde(rename = "needs_you")]
    NeedsYou,
    Working,
    Unread,
    Idle,
    Offline,
    #[default]
    Unknown,
}

impl AgentState {
    /// Client precedence: a present `state` wins, including an unknown value; null or absent falls back to `status`.
    /// The client never derives this from seen time or pending interactions.
    pub fn from_client(state: Option<&str>, status: Option<&str>) -> Self {
        match state {
            Some(value) => Self::parse(value),
            None => match status {
                Some("blocked") => Self::NeedsYou,
                Some("working") => Self::Working,
                Some("done") => Self::Unread,
                Some("idle") => Self::Idle,
                Some("offline") => Self::Offline,
                _ => Self::Unknown,
            },
        }
    }

    /// The hub maps host status, pending interactions and its separate seen record (`assistant/fleet.go: agentState`).
    pub fn from_hub(input: &HubSession<'_>) -> Self {
        if input.pending_interactions > 0 || input.status == "blocked" {
            return Self::NeedsYou;
        }
        match input.status {
            "working" => Self::Working,
            "done" => match (input.updated_at, input.seen_at) {
                (Some(updated), Some(seen)) if seen >= updated.saturating_sub(2_000_000_000) => Self::Idle,
                _ => Self::Unread,
            },
            "idle" => Self::Idle,
            "offline" => Self::Offline,
            _ => Self::Unknown,
        }
    }

    fn parse(value: &str) -> Self {
        match value {
            "needs_you" => Self::NeedsYou,
            "working" => Self::Working,
            "unread" => Self::Unread,
            "idle" => Self::Idle,
            "offline" => Self::Offline,
            _ => Self::Unknown,
        }
    }

    pub fn as_str(self) -> &'static str {
        match self {
            Self::NeedsYou => "needs_you",
            Self::Working => "working",
            Self::Unread => "unread",
            Self::Idle => "idle",
            Self::Offline => "offline",
            Self::Unknown => "unknown",
        }
    }

    pub fn is_live(self) -> bool {
        self != Self::Offline
    }

    pub fn is_quiet(self) -> bool {
        matches!(self, Self::Idle | Self::Unknown)
    }
}

/// The parts of an iOS session used by the unified-state projection. Dates stay in their wire form.
#[derive(Clone, Debug, Deserialize, Eq, PartialEq)]
pub struct ClientSession {
    #[serde(default)]
    pub machine_id: String,
    pub session_id: String,
    #[serde(default)]
    pub state: Option<String>,
    #[serde(default)]
    pub status: Option<String>,
    #[serde(default)]
    pub state_since: Option<String>,
    #[serde(default)]
    pub updated_at: Option<String>,
    #[serde(default)]
    pub waiting: Option<bool>,
    #[serde(default)]
    pub background: Option<Vec<serde_json::Value>>,
    #[serde(rename = "loop", default)]
    pub loop_info: Option<serde_json::Value>,
    #[serde(default)]
    pub subagents: Option<Vec<Subagent>>,
    #[serde(default)]
    pub workflows: Option<Vec<WorkflowRun>>,
    #[serde(default)]
    pub work: Option<WorkState>,
    #[serde(default)]
    pub workflow_progress: Option<WorkflowRun>,
    #[serde(default)]
    pub app_hub_group: Option<String>,
}

/// Only running children make a quiet session waiting.
#[derive(Clone, Debug, Deserialize, Eq, PartialEq)]
pub struct Subagent {
    pub running: bool,
}

/// A workflow's explicit running flag wins; an older hub's status is the fallback.
#[derive(Clone, Debug, Deserialize, Eq, PartialEq)]
pub struct WorkflowRun {
    #[serde(default)]
    pub task_id: Option<String>,
    #[serde(default)]
    pub run_id: Option<String>,
    #[serde(default)]
    pub running: Option<bool>,
    #[serde(default)]
    pub status: Option<String>,
}

impl WorkflowRun {
    fn is_running(&self) -> bool {
        if self.task_id.is_none() && self.run_id.is_none() {
            return false;
        }
        self.running.unwrap_or(self.status.as_deref() == Some("running"))
    }
}

/// The hub's triage fields that say it will wake a quiet session.
#[derive(Clone, Debug, Deserialize, Eq, PartialEq)]
pub struct WorkState {
    pub phase: String,
    #[serde(default)]
    pub on: Option<String>,
    #[serde(default)]
    pub wait: Option<String>,
    #[serde(default)]
    pub wake_at: Option<String>,
    #[serde(default)]
    pub agent: Option<serde_json::Value>,
}

impl WorkState {
    fn hub_waits(&self) -> bool {
        if self.phase != "blocked" || self.on.as_deref() == Some("user") {
            return false;
        }
        self.wake_at.is_some()
            || (self.on.as_deref() == Some("external")
                && self.wait.as_deref().unwrap_or("").is_empty()
                && self.agent.is_none())
    }
}

/// The row's id, decoded state and the fields the Agents list derives from them.
#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct SessionState {
    pub id: String,
    pub state: AgentState,
    pub is_live: bool,
    pub is_quiet: bool,
    pub is_waiting: bool,
    /// `None` represents the client's distant-past sort date.
    pub sort_date: Option<String>,
}

/// Decode a client session. A summary row's `waiting` replaces the detailed waiting derivation, as on iOS.
pub fn session_state(session: &ClientSession) -> SessionState {
    let state = AgentState::from_client(session.state.as_deref(), session.status.as_deref());
    let group = session.app_hub_group.as_deref().unwrap_or("");
    let prefix = if group.is_empty() {
        String::new()
    } else {
        format!("{group}|")
    };
    let detailed_waiting = session.loop_info.is_some()
        || session.background.as_ref().is_some_and(|tasks| !tasks.is_empty())
        || session
            .subagents
            .as_ref()
            .is_some_and(|agents| agents.iter().any(|agent| agent.running))
        || session.work.as_ref().is_some_and(WorkState::hub_waits)
        || running_workflows(session);
    let is_quiet = state.is_quiet();
    SessionState {
        id: format!("{prefix}{}/{}", session.machine_id, session.session_id),
        state,
        is_live: state.is_live(),
        is_quiet,
        is_waiting: is_quiet && session.waiting.unwrap_or(detailed_waiting),
        sort_date: if state == AgentState::Working {
            session.state_since.clone().or_else(|| session.updated_at.clone())
        } else {
            session.updated_at.clone()
        },
    }
}

fn running_workflows(session: &ClientSession) -> bool {
    if session.waiting.is_some() {
        return session.workflow_progress.as_ref().is_some_and(WorkflowRun::is_running);
    }
    session
        .workflows
        .as_ref()
        .is_some_and(|runs| runs.iter().any(WorkflowRun::is_running))
}

/// The hub-side session fields that participate in its unified state. Timestamps are nanoseconds from the Unix epoch.
pub struct HubSession<'a> {
    pub status: &'a str,
    pub pending_interactions: i64,
    pub updated_at: Option<i128>,
    pub seen_at: Option<i128>,
}

/// Parse the UTC RFC 3339 shape the hub emits. This is a calendar calculation, not a clock: no current time is read.
/// The fractional part is truncated to nanoseconds. Offsets and impossible calendar fields are rejected.
pub fn utc_timestamp(value: &str) -> Option<i128> {
    let b = value.as_bytes();
    if b.len() < 20 || b.last() != Some(&b'Z') {
        return None;
    }
    let number = |start: usize, len: usize| -> Option<i128> {
        let bytes = b.get(start..start + len)?;
        if !bytes.iter().all(u8::is_ascii_digit) {
            return None;
        }
        Some(bytes.iter().fold(0_i128, |n, digit| n * 10 + i128::from(digit - b'0')))
    };
    if b.get(4) != Some(&b'-')
        || b.get(7) != Some(&b'-')
        || b.get(10) != Some(&b'T')
        || b.get(13) != Some(&b':')
        || b.get(16) != Some(&b':')
    {
        return None;
    }
    let year = number(0, 4)?;
    let month = number(5, 2)?;
    let day = number(8, 2)?;
    let hour = number(11, 2)?;
    let minute = number(14, 2)?;
    let second = number(17, 2)?;
    if !(1..=12).contains(&month)
        || day < 1
        || day > i128::from(days_in_month(year, month))
        || hour >= 24
        || minute >= 60
        || second >= 60
    {
        return None;
    }
    let fraction = if b.get(19) == Some(&b'.') {
        let digits = &b[20..b.len() - 1];
        if digits.is_empty() || !digits.iter().all(u8::is_ascii_digit) {
            return None;
        }
        digits
            .iter()
            .take(9)
            .fold(0_i128, |n, digit| n * 10 + i128::from(digit - b'0'))
            * 10_i128.pow(9_u32.saturating_sub(u32::try_from(digits.len().min(9)).ok()?))
    } else {
        if b.len() != 20 {
            return None;
        }
        0
    };
    Some(
        ((days_from_civil(year, month, day) * 86_400 + hour * 3_600 + minute * 60 + second) * 1_000_000_000) + fraction,
    )
}

fn days_in_month(year: i128, month: i128) -> u8 {
    match month {
        1 | 3 | 5 | 7 | 8 | 10 | 12 => 31,
        4 | 6 | 9 | 11 => 30,
        2 if year % 4 == 0 && (year % 100 != 0 || year % 400 == 0) => 29,
        2 => 28,
        _ => 0,
    }
}

// Howard Hinnant's proleptic-Gregorian days_from_civil, with 1970-01-01 as day zero.
fn days_from_civil(year: i128, month: i128, day: i128) -> i128 {
    let year = year - i128::from(month <= 2);
    let era = year.div_euclid(400);
    let year_of_era = year - era * 400;
    let shifted_month = month + if month > 2 { -3 } else { 9 };
    let day_of_year = (153 * shifted_month + 2) / 5 + day - 1;
    let day_of_era = year_of_era * 365 + year_of_era / 4 - year_of_era / 100 + day_of_year;
    era * 146_097 + day_of_era - 719_468
}
