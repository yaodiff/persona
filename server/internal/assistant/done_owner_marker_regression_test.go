package assistant

import (
	"context"
	"testing"
)

// Conditional guidance and quoted history are not new owner demands; independent direct demands still get through.
func TestOwnerMarkerConditionalAndQuotedOccurrences(t *testing.T) {
	for _, tc := range []struct {
		name, reply, verdict string
	}{
		{"only if is conditional", "The work is complete. Only if the release fails, you need to roll it back.", verdictDone},
		{"trailing if is conditional", "The work is complete. You need to roll it back if the release fails.", verdictDone},
		{"trailing only if is conditional", "The work is complete. You need to roll it back only if the release fails.", verdictDone},
		{"trailing even if is still direct", "The work is complete. You need to notify the owner even if the release fails.", verdictNeedsUser},
		{"trailing if does not hide an independent requirement", "You need to roll it back if the release fails. Otherwise, you need to approve the release.", verdictNeedsUser},
		{"condition on a contrasted clause does not hide the first requirement", "The work is complete. You need to approve the release, but roll it back if it fails.", verdictNeedsUser},
		{"condition after the first requirement does not hide the contrasted demand", "The work is complete. You need to roll it back if the release fails, but you need to notify the owner.", verdictNeedsUser},
		{"but inside a leading conditional stays conditional", "If the release fails, the rollback is automatic, but you need to notify the owner.", verdictDone},
		{"but inside an even-if sentence stays direct", "Even if the release fails, the rollback is automatic, but you need to notify the owner.", verdictNeedsUser},
		{"pronoun before but does not hide a contrasted owner marker", "The work is complete. Even if the release fails, the rollback is automatic for everyone but you need to notify the owner.", verdictNeedsUser},
		{"quoted leading condition does not hide a post-contrast demand", `The work is complete. The old runbook says "Note: if the release fails, roll it back", but you need to approve the release.`, verdictNeedsUser},
		{"quoted sentence period does not expose a leading condition after but", `The work is complete. The runbook says "Stop. If the release fails, roll it back", but you need to approve the release.`, verdictNeedsUser},
		{"quoted sentence period does not expose a leading condition after comma", `The work is complete. The runbook says "Stop. If the release fails, roll it back", you need to approve the release.`, verdictNeedsUser},
		{"determiner with a finite clause stays a contrast", "The work is complete. You need to approve the release but the rollback waits if it fails.", verdictNeedsUser},
		{"demonstrative pronoun with a finite clause stays a contrast", "The work is complete. You need to approve the release but this waits if it fails.", verdictNeedsUser},
		{"modified exception noun phrase stays conditional", "The work is complete. You need to notify everyone but the release owner if the release fails.", verdictDone},
		{"last but only if does not hide an earlier unconditional contrast", "The work is complete. You need to approve the release, but roll it back, but only if it fails.", verdictNeedsUser},
		{"contrast without a comma keeps the first requirement direct", "The work is complete. You need to approve the release but roll it back if it fails.", verdictNeedsUser},
		{"contrasted imperative outside the verb list stays direct", "The work is complete. You need to approve the release but deploy it if it fails.", verdictNeedsUser},
		{"earlier indefinite object does not hide a finite contrast", "The work is complete. You need to write everything down, but the rollback waits if it fails.", verdictNeedsUser},
		{"local indefinite object does not hide a finite contrast", "The work is complete. You need to review everything, but the rollback waits if it fails.", verdictNeedsUser},
		{"finite contrast verb with an object stays a contrast", "The work is complete. You need to notify everyone, but the rollback needs approval if the build fails.", verdictNeedsUser},
		{"verb-like noun head in an exception phrase stays conditional", "The work is complete. You need to review all but the final report if the build fails.", verdictDone},
		{"plural verb-like noun heads stay an exception phrase", "The work is complete. You need to review all but the final reports if the build fails.", verdictDone},
		{"plural subject with a base-form finite verb stays a contrast", "The work is complete. You need to notify everyone, but the rollbacks need approval if the build fails.", verdictNeedsUser},
		{"nobody exception phrase stays conditional", "The work is complete. You need to notify nobody but the owner if the release fails.", verdictDone},
		{"no one exception phrase stays conditional", "The work is complete. You need to notify no one but the owner if the release fails.", verdictDone},
		{"no one else exception phrase stays conditional", "The work is complete. You need to notify no one else but the owner if the release fails.", verdictDone},
		{"multi-modifier exception noun phrase stays conditional", "The work is complete. You need to notify everyone but the release review board if the release fails.", verdictDone},
		{"plural pronoun with a base-form intransitive stays a contrast", "The work is complete. You need to notify everyone, but they wait if the build fails.", verdictNeedsUser},
		{"first-person contrast clause stays a contrast", "The work is complete. You need to notify everyone, but I need approval if the build fails.", verdictNeedsUser},
		{"first-person final intransitive stays a contrast", "The work is complete. You need to notify everyone, but I wait if the build fails.", verdictNeedsUser},
		{"participial modifier stays an exception noun phrase", "The work is complete. You need to review all but the patch being tested if the build fails.", verdictDone},
		{"plural demonstrative noun phrase stays an exception", "The work is complete. You need to review all but these runs if the build fails.", verdictDone},
		{"adverb between plural subject and verb stays a contrast", "The work is complete. You need to notify everyone, but the rollbacks still need approval if the build fails.", verdictNeedsUser},
		{"participles between plural subject and verb stay a contrast", "The work is complete. You need to notify everyone, but the rollbacks being tested need approval if the build fails.", verdictNeedsUser},
		{"adjective before a plural subject head stays a contrast", "The work is complete. You need to notify everyone, but the automated rollbacks need approval if the build fails.", verdictNeedsUser},
		{"adjective in a bare exception noun phrase stays conditional", "The work is complete. You need to review all but experimental runs if the build fails.", verdictDone},
		{"post-modified bare exception noun phrase stays conditional", "The work is complete. You need to review all but experimental runs in progress if the build fails.", verdictDone},
		{"plural-looking adjective in a bare exception stays conditional", "The work is complete. You need to review all but previous runs if the build fails.", verdictDone},
		{"post-modified plural-looking adjective exception stays conditional", "The work is complete. You need to review all but previous runs in progress if the build fails.", verdictDone},
		{"base-form noun head in a bare exception stays conditional", "The work is complete. You need to review all but previous work in progress if the build fails.", verdictDone},
		{"possessive bare exception stays conditional", "The work is complete. You need to review all but its work in progress if the build fails.", verdictDone},
		{"possessive final base-form noun stays conditional", "The work is complete. You need to review all but its run if the build fails.", verdictDone},
		{"possessive modifier before a plural clause stays a contrast", "The work is complete. You need to notify everyone, but its runs wait if the build fails.", verdictNeedsUser},
		{"post-modified determiner exception stays conditional", "The work is complete. You need to review all but the status checks in progress if the build fails.", verdictDone},
		{"bare plural subject with base have stays a contrast", "The work is complete. You need to notify everyone, but owners have approval if the build fails.", verdictNeedsUser},
		{"bare plural subject with base do stays a contrast", "The work is complete. You need to notify everyone, but owners do need approval if the build fails.", verdictNeedsUser},
		{"proper-noun final intransitive stays a contrast", "The work is complete. You need to notify everyone, but Priya waits if the build fails.", verdictNeedsUser},
		{"singular analysis subject stays a contrast", "The work is complete. You need to notify everyone, but the analysis continues if the build fails.", verdictNeedsUser},
		{"singular status subject stays a contrast", "The work is complete. You need to notify everyone, but the status continues if the build fails.", verdictNeedsUser},
		{"singular series subject stays a contrast", "The work is complete. You need to notify everyone, but the series continues if the build fails.", verdictNeedsUser},
		{"plural menus subject stays a contrast", "The work is complete. You need to notify everyone, but the menus need approval if the build fails.", verdictNeedsUser},
		{"first-person past-tense predicate stays a contrast", "The work is complete. You need to notify everyone, but I was ready if the build failed.", verdictNeedsUser},
		{"first-person final past-tense predicate stays a contrast", "The work is complete. You need to notify everyone, but I was if the build failed.", verdictNeedsUser},
		{"proper-noun subject stays a contrast", "The work is complete. You need to notify everyone, but Priya needs approval if the build fails.", verdictNeedsUser},
		{"bare plural subject stays a contrast", "The work is complete. You need to notify everyone, but owners need approval if the build fails.", verdictNeedsUser},
		{"verb-like modifier in an exception noun phrase stays conditional", "The work is complete. You need to notify everyone but the review owner if the release fails.", verdictDone},
		{"nothing else but keeps the exception idiom conditional", "The work is complete. You need to do nothing else but approve if the release fails.", verdictDone},
		{"quoted period before a trailing condition does not end the scan", `The work is complete. You need to approve the release, per the "Stop." note, if the build fails.`, verdictDone},
		{"but meaning except is not a clause contrast", "The work is complete. You need to notify everyone but the owner if the release fails.", verdictDone},
		{"nothing but is not a clause contrast", "The work is complete. You need to do nothing but approve if the release fails.", verdictDone},
		{"modifier after the governing verb keeps the if-question direct", "The work is complete. You need to check carefully if the release failed.", verdictNeedsUser},
		{"but only if keeps the requirement conditional", "The work is complete. You need to roll it back, but only if the release fails.", verdictDone},
		{"but only if without a comma stays conditional", "The work is complete. You need to roll it back but only if the release fails.", verdictDone},
		{"contrasted conditional clauses stay conditional", "The work is complete. You need to roll it back if the release fails but wait if the data is intact.", verdictDone},
		{"contrast inside the condition stays conditional", "The work is complete. You need to roll it back if the release fails but the data is intact.", verdictDone},
		{"trailing if-question is a direct requirement", "The work is complete. You need to check if the release failed.", verdictNeedsUser},
		{"compound check if-question stays direct", "The work is complete. You need to check if the release failed and if the owner approved.", verdictNeedsUser},
		{"compound verify if-question stays direct", "The work is complete. You need to verify if the release failed or if the owner cancelled.", verdictNeedsUser},
		{"test object noun does not make an if-question", "The work is complete. You need to add a test if the bug reproduces.", verdictDone},
		{"check object noun does not make an if-question", "The work is complete. You need to add a check if the build fails.", verdictDone},
		{"governing check with an object keeps the trailing condition", "The work is complete. You need to check the test if the build fails.", verdictDone},
		{"adverb before check keeps the if-question direct", "The work is complete. You need to carefully check if the release failed.", verdictNeedsUser},
		{"coordinated governing verbs keep the if-question direct", "The work is complete. You need to check and verify if the release failed.", verdictNeedsUser},
		{"quoted trailing condition does not hide a direct requirement", `The work is complete. You need to approve the release, not follow the old runbook's "roll back if it fails" instruction.`, verdictNeedsUser},
		{"compound even if stays direct", "The work is complete. You need to notify the owner even if the release fails and if the owner is away.", verdictNeedsUser},
		{"trailing conditional notification is not immediate", "The work is complete. You need to let me know if the release fails.", verdictDone},
		{"otherwise after only if is direct", "Only if the release fails, you need to roll it back. Otherwise, you need to approve the release.", verdictNeedsUser},
		{"nested only-if question does not hide a direct requirement", "The report checks only if the deploy succeeded: you need to approve the release.", verdictNeedsUser},
		{"quoted direct requirement is historical", `The old runbook said, "Stop. You need to approve the release." That approval is already done.`, verdictDone},
		{"quoted direct requirement does not hide a later one", `The old runbook said, "Stop. You need to roll back." You need to approve the release.`, verdictNeedsUser},
		{"corner-quoted direct requirement is historical", `The old runbook said, 「Stop. You need to approve the release.」 That approval is already done.`, verdictDone},
		{"corner-quoted direct requirement does not hide a later one", `The old runbook said, 「Stop. You need to roll back.」 You need to approve the release.`, verdictNeedsUser},
		{"corner-quoted sentence period does not expose a trailing condition", `The work is complete. You need to approve the release, per the 「Stop.」 note, if the build fails.`, verdictDone},
		{"corner-quoted sentence period does not expose a leading condition", `The work is complete. The runbook says 「Stop. If the release fails, roll it back」, but you need to approve the release.`, verdictNeedsUser},
		{"Markdown list direct requirement", `- You need to approve the release.`, verdictNeedsUser},
		{"Markdown ordered bold direct requirement", `1. **You need to approve the release.**`, verdictNeedsUser},
		{"Markdown bold direct requirement", `**You need to approve the release.**`, verdictNeedsUser},
		{"Markdown emphasis inside the direct marker", `The work is complete. You **need to** approve the release.`, verdictNeedsUser},
		{"Markdown emphasis inside a conditional marker stays conditional", `If you **need to** roll back, run make rollback.`, verdictDone},
		{"Markdown list conditional stays conditional", `- If the release fails, you need to roll it back.`, verdictDone},
		{"Markdown emphasis does not make a narrative direct", `**The report evaluates whether you need to split the module and concludes no.**`, verdictDone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &cannedJSON{answer: `{"verdict":"done","reason":"finished"}`}
			c, err := judgeTaskDoneWith(context.Background(), m, taskDoneInput{Title: "Write the spec", Replies: []taskDoneReply{doneReply(tc.reply)}})
			if err != nil {
				t.Fatal(err)
			}
			if c.Verdict != tc.verdict || c.WouldClose {
				t.Fatalf("reply %q: verdict %q close %v, want %q false", tc.reply, c.Verdict, c.WouldClose, tc.verdict)
			}
		})
	}
}
