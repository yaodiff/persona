{{- if eq .Kind "blockers_unblocked" -}}
  {{- if eq .Lang "en" -}}All blockers on this to-do cleared. Check what can move forward.{{with .BlockerPRs}} Related upstream PRs include {{join . ", "}}.{{end}}
  {{- else if eq .Lang "ja" -}}このタスクのブロッカーはすべて解消しました。次に進められる作業を確認してください。{{with .BlockerPRs}} 関連する上流の PR は {{join . "、"}} です。{{end}}
  {{- else -}}此待办的阻塞已全部解除。请确认接下来可以推进哪些工作。{{with .BlockerPRs}} 相关上游 PR 有 {{join . "、"}}。{{end}}
  {{- end -}}
{{- else if eq .Kind "mergetrain_turn" -}}
  {{- if eq .Lang "en" -}}The preceding PR {{.PreviousPR}} {{if .PreviousClosed}}closed{{else}}merged{{end}}. PR {{.PR}} is now at the head of the merge queue. Verify checks and follow the review policy.{{if .NeedsApproval}} Human approval is still required.{{end}}
  {{- else if eq .Lang "ja" -}}先行する PR {{.PreviousPR}} が{{if .PreviousClosed}}閉じられ{{else}}マージされ{{end}}ました。PR {{.PR}} はマージキューの先頭です。チェック結果を確認し、レビュー方針に従ってください。{{if .NeedsApproval}} 人による承認が必要です。{{end}}
  {{- else -}}前一个 PR {{.PreviousPR}} 已{{if .PreviousClosed}}关闭{{else}}合并{{end}}。PR {{.PR}} 已到合并队列首位。请核对检查结果并遵守评审规则。{{if .NeedsApproval}} 此 PR 仍需人工批准。{{end}}
  {{- end -}}
{{- else if eq .Kind "write_conflict" -}}
  {{- if eq .Lang "en" -}}The declared write scope overlaps with {{.PeerTitle}} ({{.PeerID}}) at {{join .Overlaps ", "}}. Coordinate the work so the overlapping changes run in sequence.
  {{- else if eq .Lang "ja" -}}宣言された書き込み範囲が {{.PeerTitle}}（{{.PeerID}}）と {{join .Overlaps "、"}} で重複しています。重複する変更を順番に進められるよう調整してください。
  {{- else -}}声明的写入范围与「{{.PeerTitle}}」（{{.PeerID}}）在 {{join .Overlaps "、"}} 重叠。请协调相关修改按顺序进行。
  {{- end -}}
{{- end -}}
