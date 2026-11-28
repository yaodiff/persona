package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/modapi"
	"github.com/nwverse/persona/server/internal/prompts"
)

// prepareRoutineFacts runs only as part of dispatch. No provider sees private routine
// settings, the prompt, the session, or any of the built-in facts.
func (s *Service) prepareRoutineFacts(ctx context.Context, task *Task, at time.Time) {
	if !task.Facts && !task.Report {
		return
	}
	var lang string
	if source, ok := s.ext.(modapi.RoutineContributionSource); ok {
		day := at.In(s.location(ctx)).Format(time.DateOnly)
		lang = string(s.botLang(ctx))
		task.routineContributions = source.RoutineContributions(ctx, day, lang)
	}
	base := s.runFacts(ctx, *task, at)
	task.factsText = prompts.RenderRoutineContributions(base, lang, task.routineContributions)
}

const routineContributionNoticePrefix = "feed_routine_contributions_"

// Facts runs already include the contributions on their fixed brief item. For a
// report-only run or a build without the briefing module, use a plain conversation
// notice. One durable ID covers retries and a manager restart for the same occurrence.
func (s *Service) writeRoutineContributionNotice(run ScheduleRun, task Task) {
	if len(task.routineContributions) == 0 || task.Facts && s.briefingOn() {
		return
	}
	var sources []string
	for _, contribution := range task.routineContributions {
		sources = append(sources, strings.Join(contribution.PlainLines(), "\n"))
	}

	key := sha256.Sum256([]byte(run.RefID + "\x00" + run.Due.UTC().Format(time.RFC3339Nano)))
	id := routineContributionNoticePrefix + hex.EncodeToString(key[:16])
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := s.appendFeedOnce(ctx, id, run.SessionID, feedNotice, run.RefID, noticeBody{Text: strings.Join(sources, "\n\n")})
	if err != nil {
		s.logf("routine %s: could not write extension contributions: %v", run.RefID, err)
	}
}
