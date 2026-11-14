//! The session fields used by the Agents list projection.
//!
//! Kept separate from the state-only decoder: adding a list field must not change the other projection's wire type.

use serde::Deserialize;

use crate::state::WorkState;

#[derive(Clone, Debug, Default, Deserialize)]
pub struct Session {
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
    pub subagents: Option<Vec<crate::state::Subagent>>,
    #[serde(default)]
    pub workflows: Option<Vec<crate::state::WorkflowRun>>,
    #[serde(default)]
    pub work: Option<WorkState>,
    #[serde(default)]
    pub workflow_progress: Option<crate::state::WorkflowRun>,
    #[serde(default)]
    pub app_hub_group: Option<String>,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub cwd: String,
    #[serde(default)]
    pub repo_root: Option<String>,
    #[serde(default)]
    pub branch: Option<String>,
    #[serde(default)]
    pub last_text: Option<String>,
    #[serde(default)]
    pub asks: Option<String>,
    #[serde(default)]
    pub space_id: Option<String>,
    #[serde(default)]
    pub space_name: Option<String>,
    #[serde(default)]
    pub slot_name: Option<String>,
    #[serde(default)]
    pub slot_id: Option<String>,
    #[serde(default)]
    pub slot_index: Option<i64>,
    #[serde(default)]
    pub agent: Option<String>,
    #[serde(default)]
    pub agent_label: Option<String>,
    #[serde(default)]
    pub source_kind: Option<String>,
    #[serde(default)]
    pub model: Option<String>,
    #[serde(default)]
    pub launcher: Option<String>,
    #[serde(default)]
    pub start_error: Option<String>,
    #[serde(default)]
    pub turn_error: Option<String>,
    #[serde(default)]
    pub failure_reason: Option<String>,
    #[serde(default)]
    pub attention_reason: Option<String>,
    #[serde(default)]
    pub assistant: bool,
    #[serde(default)]
    pub awaits_reply: bool,
    #[serde(default)]
    pub background_waiting: bool,
}

impl Session {
    pub fn is_waiting(&self) -> bool {
        let detailed = self.loop_info.is_some()
            || self.background.as_ref().is_some_and(|tasks| !tasks.is_empty())
            || self
                .subagents
                .as_ref()
                .is_some_and(|agents| agents.iter().any(|agent| agent.running))
            || self.work.as_ref().is_some_and(|work| {
                work.phase == "blocked"
                    && work.on.as_deref() != Some("user")
                    && (work.wake_at.is_some()
                        || (work.on.as_deref() == Some("external")
                            && work.wait.as_deref().unwrap_or("").is_empty()
                            && work.agent.is_none()))
            })
            || if self.waiting.is_some() {
                self.workflow_progress
                    .as_ref()
                    .is_some_and(|run| run.running.unwrap_or(run.status.as_deref() == Some("running")))
            } else {
                self.workflows.as_ref().is_some_and(|runs| {
                    runs.iter()
                        .any(|run| run.running.unwrap_or(run.status.as_deref() == Some("running")))
                })
            };
        self.waiting.unwrap_or(detailed)
    }

    pub fn agent_label(&self) -> String {
        if self.is_codex() {
            "Codex".to_owned()
        } else if self.agent.as_deref() == Some("traex") {
            "TRAEX".to_owned()
        } else if self.is_claude() {
            "Claude".to_owned()
        } else {
            self.agent_label
                .clone()
                .or_else(|| self.agent.clone())
                .unwrap_or_else(|| "Claude".to_owned())
        }
    }

    pub fn harness_id(&self) -> String {
        if self.is_codex() {
            return "codex".to_owned();
        }
        if self.agent.as_deref() == Some("traex") {
            return "traex".to_owned();
        }
        let raw = self
            .agent
            .as_deref()
            .or(self.launcher.as_deref())
            .unwrap_or("")
            .trim()
            .to_lowercase();
        if raw.is_empty() || matches!(raw.as_str(), "claude-code" | "claudecode") {
            return "claude".to_owned();
        }
        ["claude", "codex", "opencode", "pi", "gemini", "cursor", "trae"]
            .into_iter()
            .find(|known| raw.contains(known))
            .unwrap_or(&raw)
            .to_owned()
    }

    fn is_codex(&self) -> bool {
        self.agent.as_deref() == Some("codex")
            || matches!(self.source_kind.as_deref(), Some("codex_app_server" | "codex_history"))
    }

    fn is_claude(&self) -> bool {
        !self.is_codex() && matches!(self.agent.as_deref(), None | Some("" | "claude"))
    }
}
