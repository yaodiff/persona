package assistant

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// memoryMainEntityMax caps the non-sensitive local memory attached automatically to a main-chat turn when the
// current user message mentions high-discriminability entities (projects, repos, people, machines, tools/services).
const memoryMainEntityMax = 1536

// genericEntityStopwords filters high-frequency generic nouns and scene labels so only discriminative entity names
// (project/repo names, people, machines, concrete tools/services) trigger automatic main-chat local memory.
var genericEntityStopwords = map[string]bool{
	// Generic English terms & scene/facet labels
	"persona": true, "assistant": true, "agent": true, "agents": true,
	"task": true, "tasks": true, "todo": true, "todos": true,
	"test": true, "tests": true, "testing": true, "doc": true, "docs": true, "document": true,
	"system": true, "model": true, "models": true, "config": true, "configuration": true,
	"tool": true, "tools": true, "plan": true, "plans": true, "report": true, "reports": true,
	"note": true, "notes": true, "project": true, "projects": true, "memory": true,
	"code": true, "deploy": true, "release": true, "issue": true, "issues": true,
	"fix": true, "bug": true, "feature": true, "data": true, "file": true, "files": true,
	"script": true, "scripts": true, "service": true, "services": true, "server": true, "client": true,
	"user": true, "users": true, "work": true, "chat": true, "message": true, "messages": true,
	"status": true, "review": true, "launch": true, "push": true, "pull": true, "merge": true,
	"build": true, "check": true, "dispatch": true, "ask": true, "reply": true,
	"workflow": true, "judgement": true, "interaction": true, "world": true, "people": true,
	"profile": true, "general": true, "global": true, "default": true, "main": true, "core": true,
	"update": true, "change": true, "list": true, "read": true, "write": true, "run": true, "start": true, "stop": true,
	// Generic Chinese terms & scene labels
	"任务": true, "测试": true, "文档": true, "系统": true, "模型": true, "配置": true, "工具": true, // i18n-ignore: matched against memory subjects, not display copy
	"方案": true, "实验": true, "报告": true, "笔记": true, "项目": true, "助手": true, "对话": true, // i18n-ignore: matched against memory subjects, not display copy
	"记忆": true, "平台": true, "代码": true, "开发": true, "部署": true, "发布": true, "问题": true, // i18n-ignore: matched against memory subjects, not display copy
	"修复": true, "需求": true, "计划": true, "日志": true, "数据": true, "文件": true, "目录": true, // i18n-ignore: matched against memory subjects, not display copy
	"脚本": true, "接口": true, "服务": true, "版本": true, "用户": true, "工作": true, "沟通": true, // i18n-ignore: matched against memory subjects, not display copy
	"协作": true, "汇报": true, "提问": true, "派活": true, "评审": true, "验收": true, "开工": true, // i18n-ignore: matched against memory subjects, not display copy
	"回复": true, "排期": true, "门禁": true, "待办": true, "设置": true, "消息": true, "通知": true, // i18n-ignore: matched against memory subjects, not display copy
	"提醒": true, "摘要": true, "总结": true, "规则": true, "规矩": true, "原则": true, "策略": true, // i18n-ignore: matched against memory subjects, not display copy
	"状态": true, "进展": true, "结果": true, "决定": true, "决策": true, "背景": true, "信息": true, // i18n-ignore: matched against memory subjects, not display copy
	"内容": true, "方式": true, "流程": true, "时间": true, "今天": true, "明天": true, "昨天": true, // i18n-ignore: matched against memory subjects, not display copy
	"最近": true, "现在": true, "所有": true, "全部": true, "通用": true, "全局": true, "默认": true, // i18n-ignore: matched against memory subjects, not display copy
	"其他": true, "个人": true, "主要": true, "基础": true, "核心": true, "用户侧写": true, // i18n-ignore: matched against memory subjects, not display copy
}

func isASCIIString(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func isASCIIAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func hasASCIILetter(s string) bool {
	for i := 0; i < len(s); i++ {
		if (s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z') {
			return true
		}
	}
	return false
}

// highDiscriminabilitySubject normalizes a subject and reports whether it is specific enough to trigger
// automatic entity recall without matching generic prose or short ASCII substrings.
func highDiscriminabilitySubject(raw string) (norm string, ascii bool, ok bool) {
	norm = strings.ToLower(strings.Join(strings.Fields(raw), " "))
	if norm == "" || genericEntityStopwords[norm] {
		return "", false, false
	}
	if isASCIIString(norm) {
		if len(norm) < 3 || !hasASCIILetter(norm) {
			return "", true, false
		}
		return norm, true, true
	}
	if utf8.RuneCountInString(norm) < 2 {
		return "", false, false
	}
	return norm, false, true
}

// matchASCIIWholeWord reports whether termLower occurs in textLower with non-alphanumeric boundaries on both sides.
func matchASCIIWholeWord(textLower, termLower string) bool {
	if termLower == "" || len(termLower) > len(textLower) {
		return false
	}
	start := 0
	for {
		idx := strings.Index(textLower[start:], termLower)
		if idx < 0 {
			return false
		}
		pos := start + idx
		end := pos + len(termLower)
		leftOK := pos == 0 || !isASCIIAlnum(textLower[pos-1])
		rightOK := end == len(textLower) || !isASCIIAlnum(textLower[end])
		if leftOK && rightOK {
			return true
		}
		start = pos + 1
	}
}

// matchEntityInQuery checks whether a high-discriminability subject appears in queryLower.
// Pure ASCII subjects require whole-word boundaries (and >=3 chars); org/repo also allows matching its repo segment
// when that segment itself passes the high-discriminability check.
func matchEntityInQuery(queryLower, subject string) (int, bool) {
	norm, ascii, ok := highDiscriminabilitySubject(subject)
	if !ok {
		return 0, false
	}
	if ascii {
		if matchASCIIWholeWord(queryLower, norm) {
			return len(norm), true
		}
		if slash := strings.LastIndexByte(norm, '/'); slash >= 0 && slash+1 < len(norm) {
			if repo, repoASCII, repoOK := highDiscriminabilitySubject(norm[slash+1:]); repoOK && repoASCII && matchASCIIWholeWord(queryLower, repo) {
				return len(repo), true
			}
		}
		return 0, false
	}
	if strings.Contains(queryLower, norm) {
		return utf8.RuneCountInString(norm), true
	}
	return 0, false
}

// nonSensitiveExtensionEntries returns the entries from b that conversation session may use and that carry no
// sensitive category (strictly non-sensitive and known).
func (s *Service) nonSensitiveExtensionEntries(ctx context.Context, session string, b memoryExtensionBatch) []memoryExtensionEntry {
	if len(b.Entries) == 0 {
		return nil
	}
	f := s.assembleForConversation(ctx, session, assembleStanding)
	info := s.memoryExtensionUses(ctx, b.Tagged)
	var out []memoryExtensionEntry
	for _, e := range b.Entries {
		ae := proto.AssistantEntry{
			ID:          memoryExtensionIDPrefix + e.ID,
			Text:        e.Text,
			OriginSpace: e.OriginSpace,
			Category:    e.Categories,
			Tag:         e.Tags,
			Scope:       proto.AssistantEntryScope{Executor: "any", Personal: true},
			Status:      "active",
		}
		u := info(ae)
		if !u.Known || u.sensitive() {
			continue
		}
		rule := usage(u, entryScene(u, f), extensionGrant{})
		if rule != usageAllow && rule != usageRelevant {
			continue
		}
		out = append(out, e)
	}
	return out
}

func normalizeMemoryLine(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(oneLine(prompts.ContextBody(text))), " "))
}

// standingMemoryIDsAndTexts returns the IDs and normalized lines already carried by the standing section for session,
// so entity-triggered or leader project sections never repeat standing entries.
func (s *Service) standingMemoryIDsAndTexts(ctx context.Context, session string, b memoryExtensionBatch) (map[string]bool, map[string]bool) {
	ids := map[string]bool{}
	texts := map[string]bool{}
	standing := b.standingOnly()
	if len(standing.Entries) == 0 {
		return ids, texts
	}
	a := s.assembleExtension(ctx, s.assembleForConversation(ctx, session, assembleStanding), standing)
	for _, e := range a.Entries {
		ids[e.ID] = true
		if norm := normalizeMemoryLine(e.Text); norm != "" {
			texts[norm] = true
		}
	}
	return ids, texts
}

// mainEntityMemorySection deterministically attaches up to memoryMainEntityMax bytes of relevant non-sensitive
// extension memory entries when the current main-chat user turn mentions high-discriminability entities.
func (s *Service) mainEntityMemorySection(ctx context.Context, machine, session, query string) string {
	if strings.TrimSpace(query) == "" || !s.isMain(ctx, session) || !s.ownersPersonal(ctx, machine, session) {
		return ""
	}
	b := s.cachedExtensionMemoryBatch(ctx)
	if len(b.Entries) == 0 {
		return ""
	}
	standingIDs, standingTexts := s.standingMemoryIDsAndTexts(ctx, session, b)
	visible := s.nonSensitiveExtensionEntries(ctx, session, b)
	if len(visible) == 0 {
		return ""
	}
	queryLower := strings.ToLower(query)
	type matchedEntry struct {
		entry      memoryExtensionEntry
		hitCount   int
		longestHit int
		order      int
	}
	var matches []matchedEntry
	for idx, e := range visible {
		if standingIDs[memoryExtensionIDPrefix+e.ID] {
			continue
		}
		normText := normalizeMemoryLine(e.Text)
		if normText == "" || standingTexts[normText] {
			continue
		}
		hits, longest := 0, 0
		seenSubjects := map[string]bool{}
		for _, subj := range e.Tags {
			if n, ok := matchEntityInQuery(queryLower, subj); ok {
				key := strings.ToLower(strings.TrimSpace(subj))
				if !seenSubjects[key] {
					seenSubjects[key] = true
					hits++
				}
				if n > longest {
					longest = n
				}
			}
		}
		if hits == 0 {
			continue
		}
		matches = append(matches, matchedEntry{entry: e, hitCount: hits, longestHit: longest, order: idx})
	}
	if len(matches) == 0 {
		return ""
	}
	slices.SortStableFunc(matches, func(x, y matchedEntry) int {
		if c := cmp.Compare(y.hitCount, x.hitCount); c != 0 {
			return c
		}
		if c := cmp.Compare(y.longestHit, x.longestHit); c != 0 {
			return c
		}
		if c := cmp.Compare(y.entry.Rank, x.entry.Rank); c != 0 {
			return c
		}
		return cmp.Compare(y.order, x.order)
	})
	left := memoryMainEntityMax
	seenNorm := map[string]bool{}
	d := prompts.StandingMemory{Language: answerLanguage(s.hello(ctx).Locale)}
	for _, m := range matches {
		line := oneLine(prompts.ContextBody(m.entry.Text))
		if line == "" {
			continue
		}
		norm := normalizeMemoryLine(line)
		if seenNorm[norm] {
			continue
		}
		if len(line) > left {
			continue
		}
		seenNorm[norm] = true
		left -= len(line)
		d.Entries = append(d.Entries, prompts.MemoryEntry{Text: line})
		if left == 0 {
			break
		}
	}
	return prompts.RenderEntityMemory(d)
}

func appendSnapshotSection(snapshot, section string) string {
	if section == "" {
		return snapshot
	}
	if snapshot != "" && !strings.HasSuffix(snapshot, "\n") {
		snapshot += "\n"
	}
	return snapshot + section + "\n"
}
