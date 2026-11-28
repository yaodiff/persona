📋 督办：你派出去的任务有 {{len .Entries}} 个要你处理
（这是 Persona 的督办消息，不是用户发的。{{if .Background}}仅收尾旧任务；新任务归主管或主对话。{{else if .Leader}}你督办自己派出和转交给你的任务；仅需用户决定时升级。{{else if .Side}}你督办自己派出的任务。{{end}}）
{{if .Fresh}}这一轮通常从干净的上下文开始：之前几轮的对话不一定在你眼前，跨轮要记的都在任务台账里（简报、范围调整、已定的事、上次结论），需要时用 todo_get 看。
{{end}}{{if .Asked}}Question routing: timeout_minutes={{.AskMinutes}}, low_distraction={{.PushGrading}}.
{{end}}<persona-supervision level="{{if .Level}}{{.Level}}{{else}}triage{{end}}" />
{{if .Fresh}}<persona-supervision-round />
{{end}}{{range $i, $e := .Entries}}
{{inc $i}}. 任务「{{$e.Title}}」（{{$e.ID}}，signal={{$e.Kind}}{{if $e.Reported}}+report{{end}}{{with $e.Link}}，链接 {{.}}{{end}}{{with $e.OriginID}}，来源对话：{{with $e.OriginTitle}}「{{.}}」（{{$e.OriginID}}）{{else}}{{$e.OriginID}}{{end}}{{with $e.OriginLink}}，链接 {{.}}{{end}}{{end}}）：{{if eq $e.Kind "review"}}待逐项复核，不代表完成{{else if eq $e.Kind "review_nudge"}}已等待你验收超过 2 小时（已等 {{$e.Minutes}} 分钟，目前仍留在进行中未打扰用户）；请尽快用 task_review 复核，满 24 小时将自动浮出到用户的「需要你」{{else if eq $e.Kind "idle"}}Agent 停下了，但没做完{{else if eq $e.Kind "unjudged"}}Agent 停下了，hub 没判断出来是不是做完了（不是说没做完）{{else if eq $e.Kind "needs_you"}}Agent 在等回答{{with $e.Minutes}}，等了 {{.}} 分钟{{end}}{{if $e.Quiet}}（用户没收到推送，只有你知道）{{end}}{{else if eq $e.Kind "pr_conflict"}}当前任务的 PR 有合并冲突，暂时无法合并{{else if eq $e.Kind "all_stopped"}}{{template "shared/all_stopped" $e}}{{else if eq $e.Kind "stalled"}}{{$e.Minutes}} 分钟没有进展{{else if eq $e.Kind "no_progress"}}{{$e.Minutes}} 分钟没有真实进展：会话一直在写，但 hub 看到的 {{$e.Calls}} 次工具调用里有 {{$e.Waits}} 次是空等、查状态或重复同一条命令，其间没有改文件、提交，也没有带证据的汇报或验收读数变化{{else if eq $e.Kind "timed_out"}}超时没有主管处理或真实进展，Agent 已停止；原会话和工作区保留，Continue 继续原会话{{else if eq $e.Kind "interrupted"}}被模型服务出错打断了{{else if eq $e.Kind "orphaned"}}没有 Agent 在做了：{{if eq $e.Orphan "error"}}它的 Agent 被模型服务报错打断，之后标签被关掉，hub 不会自己重试{{else if eq $e.Orphan "offline"}}它的 Agent 所在的电脑离线了{{else if eq $e.Orphan "queued"}}任务仍在等待可用的电脑或工作区{{else}}它的 Agent 的标签已经关闭（用户关的，或闲置太久被关），也没有排定的唤醒{{end}}{{else if eq $e.Kind "recovered"}}节点恢复巡检报告{{with $e.Minutes}}（节点离线约 {{.}} 分钟）{{end}}{{else if eq $e.Kind "started"}}重试后 Agent 开起来了，之前没开起来是因为{{else if eq $e.Kind "failed"}}Agent 没开起来{{with $e.KeptUntil}}，它的启动标签在电脑上保留到 {{.}}{{end}}{{with $e.Question}}：新标签里的 shell 停在一个提问上「{{.}}」{{end}}{{else if eq $e.Kind "overdue"}}超过约定时间还没有结束：从派出（或上次调整范围）算起已经 {{$e.Minutes}} 分钟{{else if eq $e.Kind "leader_stuck"}}归一个主管督办，卡住了{{else if eq $e.Kind "escalated"}}{{if $e.Background}}跟进它的后台督办对话{{else}}负责它的 leader 对话{{end}}判断要用户决定，{{if $e.Asks}}它的 Agent 在等的卡片已经发给用户{{else}}结论卡已经发给用户{{end}}{{else if eq $e.Kind "decide"}}{{if $e.SupervisorGone}}负责它的主管对话一直收不到这条督办，需要主对话接管处理{{else if $e.LeaderGone}}负责它的 task leader（一个编码 Agent）联系不上了，没法验收它{{else}}它的 leader（一个编码 Agent，不是对话）判断要用户决定，没有卡片发给用户{{end}}{{else if eq $e.Kind "ask"}}它的 Agent 有卡片在等，先交给你处理，用户还没收到{{else if eq $e.Kind "recheck"}}要用户拍板的决定等了超过一天还没人回答，已交回督办{{else if eq $e.Kind "undecidable"}}标着等用户决定，但用户那边没有能回答的东西（没有问题、选项或推荐）{{else if eq $e.Kind "cardless_wait"}}这个做完的任务已经等你评审 {{$e.Minutes}} 分钟，用户那边却没有任何卡片（没有 needs_user 决定、user_check 验收或待回答的提问卡）；hub 分不清这是你晚评审还是真要用户决定。立刻用 task_review 处理：不需要用户参与就 accepted、rework 或 cancelled；确实要用户拍板/验收才发 needs_user（带 reason/recommend/options/cost_of_waiting）或 accepted 加 user_check。聊天正文里的话不进 Needs you，不能只在聊天里请用户看{{else if eq $e.Kind "delivery_hold"}}Agent 收尾说上线要等用户点头{{with $e.Minutes}}（{{.}} 分钟前）{{end}}，但用户那边没有能回答的卡片，hub 没把它放进「要你处理」{{else if eq $e.Kind "decision_reply"}}{{template "assistant/decision_agent_reply" $e.AgentReply}}{{else if eq $e.Kind "owner_cannot_answer"}}用户回答不了你发的决定，回的是{{if eq $e.Response "decide_yourself"}}「你来定」{{else if eq $e.Response "not_what_i_asked"}}「这不是我要的」{{else}}「先讲清背景」{{end}}{{else if eq $e.Kind "park_decided"}}搁置期间用户在任务上记下了新的决定，搁置已经结束{{with $e.Parked}}；这条消息到之前你又搁置了它，现在在等「{{.Reason}}」，到 {{.Until}}{{end}}{{else if eq $e.Kind "parked_summary"}}Daily automatic-parking summary (information only; keep unchanged waits parked){{with $e.Parked}}: {{.Reason}}; deadline {{.Until}}; unchanged attempt {{.Count}}{{end}}{{else if eq $e.Kind "parked_due"}}搁置到期了{{with $e.Parked}}：你搁置时写的是在等「{{.Reason}}」（{{if eq .WaitingOn "owner_priority"}}等用户定优先级{{else if eq .WaitingOn "someone_else"}}等别人{{else if eq .WaitingOn "external_event"}}等外部的事{{else}}等到时间{{end}}），搁置到 {{.Until}}；已经连续搁置 {{.Count}} 次，剩余搁置次数 {{.Left}}{{if .Left}}（中间 Agent 不干活时）{{else}}（中间 Agent 没干活，不能再搁置了）{{end}}{{if .Resent}}；第 {{.Resent}} 次重发，上次提醒后没有 task_review{{end}}{{end}}{{else if eq $e.Kind "inbox"}}收件箱里有别人发来的新消息说到它{{with $e.Inbox}}{{if gt .Count 1}}（{{.Count}} 条）{{end}}{{end}}{{else if eq $e.Kind "check"}}挂在它上面的定时检查{{if eq $e.Report "decided"}}发起的决定用户回答了：选了「{{$e.Response}}」，{{if $e.Told}}这个回答已经在 Agent 的对话里{{else}}这个回答没有转给 Agent{{end}}{{else if eq $e.Report "error"}}没能保存进展记录{{else}}没看到进展{{with $e.Minutes}}（已 {{.}} 分钟）{{end}}{{end}}；这是 hub 的检查，不是 Agent 的汇报{{else if eq $e.Kind "report"}}Agent 主动向你汇报：{{if eq $e.Report "question"}}要你拍板{{else if eq $e.Report "blocked"}}卡住了{{else if eq $e.Report "done"}}说做完了，交差{{else}}进度{{end}}{{with $e.Step}}（第 {{.}}{{with $e.Steps}}/{{.}}{{end}} 步）{{end}}{{else if eq $e.Kind "todo_ready"}}前置依赖已全部满足，待推进（未自动启动，请按需评估并派发）{{else if eq $e.Kind "needs_user_external_info"}}{{template "assistant/needs_user_external_info" $e.ExternalInfo}}{{else if eq $e.Kind "owner_present"}}用户已在线或刚发来消息，这件事正在等用户本人操作，请立即当场发卡（如密码卡或确认卡）让用户处理{{end}}{{with $e.Reported}}；停下前它主动向你汇报{{if eq . "question"}}要你拍板{{else if eq . "blocked"}}卡住了{{else if eq . "done"}}说做完了{{else}}了进度{{end}}（同一轮，合成这一条）{{end}}{{with $e.ParentCloseout}}{{.}}{{end}}{{with $e.OutageRecovery}}{{.}}{{end}}{{with $e.Detail}}（{{.}}）{{end}}{{with $e.RetryWith}}；可换启动器：{{join . "、"}}{{end}}{{with $e.Machine}}；在 {{.}}{{end}}{{with $e.Session}} session_id={{.}}{{end}}{{if $e.Round}}；已返工 {{$e.Round}} 轮{{end}}
{{with $e.DoneWhen}}   验收标准用 todo_get 取原文{{else}}   验收标准：未记录{{end}}{{with $e.TodoReady}}{{with .PartOf}}
   所属需求：{{.}}{{end}}{{with .Satisfied}}
   已满足前置：{{range .}}
   - {{.Title}}（{{.ID}}，{{if eq .Reason "pr_merged"}}PR 已合并{{else}}已完成{{end}}{{with .MergedPRs}}：{{join . "、"}}{{end}}）{{end}}{{end}}{{with .Notes}}
   待办备注 / 启动说明：
{{chomp (indent "   " .)}}{{end}}{{end}}{{with $e.Asked}}
   最初的要求（{{$e.AskedAt}} 用户原话）：{{.}}{{if $e.AskedCut}}（只引了开头，全文见{{if $e.Link}}任务链接或{{end}} todo_get）{{end}}{{end}}{{with $e.Started}}
   这件事的由来（不是用户直接要的）：{{if eq . "leader"}}主管对话{{else if eq . "routine"}}定时任务{{else if eq . "hub"}}Persona 自己{{else}}助手{{end}}发起{{with $e.StartedWhy}}，理由：{{.}}{{end}}{{end}}{{with $e.Dropped}}
   范围调整时已去掉的标准：{{range $j, $c := .}}{{if $j}}；{{end}}{{$c}}{{end}}{{end}}{{if $.Fresh}}{{with $e.LastReview}}
   上次结论（{{.At}}）：{{.Verdict}}{{with .Note}}，{{.}}{{end}}{{end}}{{end}}{{with $e.Inbox}}
   收件箱事件 {{.Item}}「{{.Title}}」{{with .Sender}}，最新一条是 {{.}} 发的{{end}}{{with .Preview}}：「{{.}}」{{end}}{{with .URL}}（{{.}}）{{end}}{{if .AfterAcceptance}}。待办已验收，PR 尚未关闭。请查看这条反馈，保留原验收记录{{end}}{{if eq .ToAgent "sent"}}；hub 已经把要点和链接直接发给它的 Agent（它收到了），这里是副本{{end}}{{end}}{{with $e.Recommend}}
   {{if $e.Background}}后台督办对话{{else}}leader {{end}}推荐：{{.}}{{end}}{{with $e.Decision}}
   当时发给用户的决定（why_owner={{.WhyOwner}}{{if .NeverDefault}}，不会默认执行{{end}}）：{{.Question}}{{with .Background}}
   - 任务来历（hub 记的）：{{.}}{{end}}{{range .Options}}
   - 选项「{{.Label}}」：{{.LeadsTo}}{{end}}{{with .Recommend}}
   - 推荐：{{.}}{{end}}{{with .Cost}}
   - 等待的代价：{{.}}{{end}}{{end}}{{range $e.Asks}}
   - 卡片 interaction_id={{.ID}}（machine={{.Machine}}，{{if eq .Kind "permission"}}权限{{else if eq .Kind "plan"}}计划{{else if eq .Kind "screen"}}终端对话框，由你来选：interaction_answer 的 answers 填它的问题→选项名或序号，Yes/No 也可用 allow；信任文件夹、Bypass Permissions、可能删文件或以用户名义对外的归用户，用 ask_escalate{{else}}问题{{end}}，session_id={{.Session}}{{if .Reminded}}，到时间没人处理；不会自动发给用户，再交给你处理{{else if .Raised}}，下一层没有及时处理，上交到你这里{{end}}）：
{{chomp (indent "     " .Text)}}{{end}}{{with $e.Decisions}}
   这个任务已经定下的事：
{{chomp (indent "   " .)}}{{end}}{{with $e.Ask}}
   它在问：
{{chomp (indent "   " .)}}{{end}}{{with $e.Said}}
   它说：
{{chomp (indent "   " .)}}{{end}}{{with $e.Evidence}}
   它给的证据：{{range .}}
   - {{.}}{{end}}{{end}}{{with $e.Files}}
   它交回的文件（已经放到你这台电脑上，直接读；不要让它贴内容）：{{range .}}
   - {{.Name}}{{if .Error}}（没取回：{{.Error}}；在原电脑 {{.Path}}）{{else}}：{{.Path}}{{end}}{{end}}{{end}}{{with $e.Closeout}}
   它自己填的收尾记录（它的说法，没人核对过）：{{if eq .Status "done"}}做完了{{else if eq .Status "blocked"}}卡住了{{with .Waiting}}，在等{{.}}{{end}}{{else}}没做完{{end}}；合并：{{if eq .Merged "yes"}}是{{else if eq .Merged "no"}}否{{else}}没说或不适用{{end}}；部署：{{if eq .Deployed "yes"}}是{{else if eq .Deployed "no"}}否{{else}}没说或不适用{{end}}{{if or .CheckedMerged .CheckedDeployed}}
   - hub 自己查了它的 PR：{{if eq .CheckedMerged "yes"}}已合并{{else if eq .CheckedMerged "no"}}没合并{{else}}合没合查不出{{end}}，{{if .LocalDeployment}}本机部署包含该 PR 的改动{{else if eq .CheckedDeployed "yes"}}线上正在跑的版本里有它{{else if eq .CheckedDeployed "no"}}线上正在跑的版本里没有它{{else}}上没上线查不出{{end}}{{end}}{{range .Did}}
   - 做了：{{.}}{{end}}{{range .Artifacts}}
   - 产物：{{.}}{{end}}{{with .Verified}}
   - 验证：{{.}}{{end}}{{with .Missing}}
   - 还缺：{{.}}{{end}}{{end}}{{with $e.Last}}
   Agent 最后说的：
{{chomp (indent "   " .)}}{{end}}{{with $e.Screen}}
   启动标签最后几行：
{{chomp (indent "   | " .)}}{{end}}{{if $e.KeptUntil}}
   还能重试 {{$e.RetriesLeft}} 次。{{end}}{{with $e.Review}}
   独立评审（评审员只看了任务要求和改动，看不到 Agent 的推理）：{{if eq .State "failed"}}没评审成（{{.Error}}）{{else if eq .State "clean"}}没有阻断问题{{with .Count}}，{{.}} 条非阻断发现{{end}}{{else}}返工轮数用完了，还剩 {{.Blocking}} 条阻断问题{{end}}{{with .Findings}}
{{chomp (indent "   " .)}}{{end}}{{end}}
{{end}}
