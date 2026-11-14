⏰ {{.Label}}{{if .Once}}（你之前安排的跟进）{{else if .Interval}}（定时任务，每 {{.Every}}）{{else}}（定时任务）{{end}}{{if .Late}}
（本该 {{.Due.Format "01-02 15:04"}} 运行，Persona 当时没在运行，晚了 {{.Late}}；看看还有没有必要按原计划做。）{{end}}{{if .Continued}}
（又到时间了，还是这个任务自己的对话：前几次的结果都在上面，接着做，和上次比有变化的地方说清楚。）{{else if .Interval}}
（Persona 会按时再运行它，不用再用 schedule_followup 安排下一次。）{{end}}{{if .PushAsked}}
（这个任务只在你用 notify_user 时才推送到手机：有用户需要知道的事就用 notify_user 推一条，没有就简短说一句，这次运行不会推送。）{{end}}{{if .InboxWorthy}}
（这个任务设了「有值得看的才放进收件箱」：只有发现用户需要看的事（高价值的信息、变化、要他处理的）才用 inbox_post 放一行进收件箱，没有就简短说一句；普通结果不要调用。）{{else if .InboxEvery}}
（这个任务设了「每次都放进收件箱」：运行结束后 Persona 会自己放一行进收件箱，你不用调用 inbox_post，把结果写清楚就行。）{{end}}{{if .Facts}}

{{.Facts}}{{end}}

{{.Prompt}}{{if and .KBHint (not .Continued)}}

{{template "assistant/routine_kb_hint" .}}{{end}}
