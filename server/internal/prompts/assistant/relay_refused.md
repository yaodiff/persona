{{if eq .Language "English"}}🔔 The message you sent with {{.Tool}} to the conversation "{{.Title}}" ({{.ID}}) was not delivered: {{if eq .Code "gone"}}that conversation is no longer available ({{.Code}}){{else}}that conversation refused it ({{.Code}}){{end}}, and it will not be sent again by itself.{{with .Excerpt}}
It began: {{.}}{{end}}
(This is a notice from Persona, not a message from the user, nor from that conversation; you are told about this message only once.)
If it still matters, take it over yourself or tell the user why it did not get through. If nothing needs doing, no reply is needed.
{{- else if eq .Language "日本語"}}🔔 {{.Tool}} で会話「{{.Title}}」（{{.ID}}）に送ったメッセージは届きませんでした。{{if eq .Code "gone"}}その会話はもう存在しないため（{{.Code}}）{{else}}その会話が受け取りを拒否したため（{{.Code}}）{{end}}、自動では再送されません。{{with .Excerpt}}
メッセージの冒頭：{{.}}{{end}}
（これは Persona からの通知で、ユーザーやその会話からのメッセージではありません。このメッセージについての通知はこの一度だけです。）
まだ必要なら自分で引き継ぐか、届かなかった理由をユーザーに伝えてください。何もすることがなければ返答は不要です。
{{- else}}🔔 你用 {{.Tool}} 发给对话「{{.Title}}」（{{.ID}}）的消息没有送到：{{if eq .Code "gone"}}那段对话已不存在（{{.Code}}）{{else}}那段对话拒收了它（{{.Code}}）{{end}}，不会再自动重发。{{with .Excerpt}}
消息开头：{{.}}{{end}}
（这是 Persona 的通知，不是用户发的消息，也不是那段对话发来的；这条消息只通知这一次。）
还需要它，就自己接手，或者告诉用户为什么没送到；没什么要做的就不用回应。
{{- end}}
