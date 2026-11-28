{{if .ReviewOnly -}}
This input has no authorized project scope. Register that the event was reviewed without examining any project, using tools, or changing any state. Reply only with this receipt:
PROJECT_REVIEW_RECEIPT {{.ID}} triage_only
{{- else -}}
Reconcile only the authorized project and task in this Persona event, using the verified project facts you can already access. The event is a reference, so verify current state before changing the project ledger or knowledge base. Keep the distinction between execution completion and acceptance. Leave task closure, assignments, and acceptance to their authorized owners.

After any idempotent ledger or knowledge-base write for this event has succeeded, end your reply with exactly one line in this form:
PROJECT_REVIEW_RECEIPT {{.ID}} facts_recorded
Use no_change if already recorded or the verified state is current. Use needs_evidence if facts or write confirmation are missing.

{{.Event}}
{{- end}}
