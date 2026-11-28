package screen

import (
	"strings"
	"testing"
)

// Codex 0.167+ active-turn compact layouts put the model/path status row and
// "tab to queue message" (with or without warnings or agent hints) directly
// below the composer without a blank separator. These footer rows must not be
// misread as typed draft text, while genuine user drafts above them stay intact (#2133).
func TestReadCodexActiveTurnQueueFooter(t *testing.T) {
	footers := []struct {
		name   string
		footer string
	}{
		{
			name: "two_row_tab_to_queue",
			footer: "  GPT-6.1-Sol xhigh · ~/work/repo · main\n" +
				"  tab to queue message\n",
		},
		{
			name: "two_row_tab_to_queue_with_context_and_warning",
			footer: "  GPT-6.1-Sol xhigh · /work/repo · Fix composer\n" +
				"  tab to queue message · 92% context left                     ⚠ 1 warning · f2 to view\n",
		},
		{
			name: "two_row_agents_and_tab_to_queue",
			footer: "  GPT-6.1-Sol max · ~/work/repo\n" +
				"  ← for agents · tab to queue message\n",
		},
		{
			name: "wrapped_status_above_tab_to_queue",
			footer: "  GPT-6.1-Sol max Fast · /home/me/work/example-project · Read the integration\n" +
				"  plan · Main [default]\n" +
				"  tab to queue message\n",
		},
		{
			name:   "single_row_status_with_tab_to_queue",
			footer: "  GPT-6.1-Sol xhigh · ~/work/repo · tab to queue message\n",
		},
	}

	for _, f := range footers {
		for _, tc := range []struct {
			name     string
			composer string
			want     string
		}{
			{"empty_placeholder", "› Ask Codex to do anything\n", ""},
			{"empty_bare", "›\n", ""},
			{"empty_dim", "› \x1b[2mAsk Codex to do anything\x1b[0m\n", ""},
			{"typed", "› check the build\n", "check the build"},
			{"multiline", "› check the build\n  and keep my changes\n", "check the build\nand keep my changes"},
			{"pasted_chip_with_suffix", "› [Pasted Content 412 chars] #2\n", "[Pasted Content 412 chars] #2"},
			{"pasted_reply", "› <send_user_message_question_reply>\n  Red\n  </send_user_message_question_reply>\n", "<send_user_message_question_reply>\nRed\n</send_user_message_question_reply>"},
		} {
			t.Run(f.name+"/"+tc.name, func(t *testing.T) {
				s := ReadCodex("• Working (12s • esc to interrupt)\n\n" + tc.composer + f.footer)
				if s.View != CodexComposer || s.Draft != tc.want {
					t.Fatalf("view=%q draft=%q, want view=%q draft=%q", s.View, s.Draft, CodexComposer, tc.want)
				}
			})
		}
	}

	// Real drafts containing footer-like text must remain protected.
	protected := []struct {
		name   string
		screen string
		want   string
	}{
		{
			name: "footer_like_inside_multiline_draft",
			screen: "› keep this text\n" +
				"  GPT-6.1-Sol xhigh · /work/repo\n" +
				"  tab to queue message\n" +
				"  and this final line\n" +
				"  GPT-6.1-Sol xhigh · ~/work/repo\n" +
				"  tab to queue message\n",
			want: "keep this text\nGPT-6.1-Sol xhigh · /work/repo\ntab to queue message\nand this final line",
		},
		{
			name: "tab_to_queue_without_status_row_stays_draft",
			screen: "› keep this text\n" +
				"  plan · Main [default]\n" +
				"  tab to queue message\n",
			want: "keep this text\nplan · Main [default]\ntab to queue message",
		},
	}
	for _, tc := range protected {
		t.Run(tc.name, func(t *testing.T) {
			s := ReadCodex("• Working (12s • esc to interrupt)\n\n" + tc.screen)
			if s.View != CodexComposer || s.Draft != tc.want {
				t.Fatalf("view=%q draft=%q, want view=%q draft=%q", s.View, s.Draft, CodexComposer, tc.want)
			}
			if !strings.Contains(s.Draft, "keep this text") {
				t.Fatalf("lost draft text: %q", s.Draft)
			}
		})
	}
}
