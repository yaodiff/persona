package wire

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDuplicateComponentKeySurvivesTypeChecks(t *testing.T) {
	for _, last := range []Component{
		{Type: "chart", Kind: "line", Series: []ChartSeries{{Points: []ChartPoint{{Label: "a", Value: 1}}}}},
		{Type: "stat_grid", Stats: []Stat{{Label: "Count", Value: "1"}}},
		{Type: "markdown", Text: "Example"},
	} {
		last.Key = "same"
		v := &View{Sections: []Section{{Items: []Component{{Type: "text", Key: "same", Text: "first"}, last}}}}
		if err := CheckView(v); err == nil || !strings.Contains(err.Error(), "key") {
			t.Fatalf("%s: %v", last.Type, err)
		}
	}
}

func TestSafeMarkdownReferences(t *testing.T) {
	for _, text := range []string{
		"![x][img]\n\n[img]: https://example.com/image",
		"[x][ref]\n\n[ref]: javascript:alert(1)",
		"[ref]:\n javascript:alert(1)\n[x][ref]",
		"<javascript:alert(1)>",
		"[bad](https://:443)",
	} {
		if err := CheckView(&View{Sections: []Section{{Items: []Component{{Type: "markdown", Text: text}}}}}); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
	for _, text := range []string{"[x](https://example.com)", "<https://example.com>", "`![x][img]`", "```\n[ref]: javascript:alert(1)\n```"} {
		if err := CheckView(&View{Sections: []Section{{Items: []Component{{Type: "markdown", Text: text}}}}}); err != nil {
			t.Errorf("%q: %v", text, err)
		}
	}
}

func TestCheckMarkdownSecurityBypasses(t *testing.T) {
	rejected := []struct {
		name string
		text string
	}{
		{"midline_unclosed_triple_backtick_link", "note ``` [click](javascript:alert(1))"},
		{"midline_unclosed_triple_backtick_image", "note ``` ![exfil](https://evil.example/t.png)"},
		{"backtick_in_fence_info_string", "``` a`\n[click](javascript:alert(1))"},
		{"multi_backtick_code_span_desync", "` ```` ` [click](javascript:alert(1))"},
		{"list_item_indented_fence_escape", "- item\n  ```\n[click](javascript:alert(1))\n  ```"},
		{"html_comment_fence_smuggle", "<!--\n```\n-->\n[click](javascript:alert(1))\n```"},
		{"html_block_fence_smuggle", "<div>\n```\n</div>\n\n[click](javascript:alert(1))\n```"},
		{"code_span_across_blank_line", "`code\n\n[click](javascript:alert(1))\n\n`"},
		{"code_span_across_blockquote", "`code\n> [click](javascript:alert(1))\n`"},
		{"blockquote_ref_def", "> [ref]: javascript:alert(1)\n\n[click][ref]"},
		{"nested_blockquote_ref_def", "> > [ref]: https://example.com\n\n[click][ref]"},
		{"bullet_list_ref_def", "- [ref]: javascript:alert(1)\n\n[click][ref]"},
		{"ordered_list_ref_def", "1. [ref]: javascript:alert(1)\n\n[click][ref]"},
		{"ordered_paren_blockquote_ref_def", "1) > [ref]: javascript:alert(1)\n\n[click][ref]"},
		{"multiline_ref_label", "[foo\nbar]: javascript:alert(1)\n\n[click][foo bar]"},
		{"blockquote_multiline_ref_label", "> [foo\n> bar]: javascript:alert(1)\n\n[click][foo bar]"},
		{"escaped_bracket_ref_label", `[a\]b]: javascript:alert(1)` + "\n\n" + `[click][a\]b]`},
		{"html_entity_userinfo_spoof_dec", "[click](https://good.example&#64;evil.example/p)"},
		{"html_entity_userinfo_spoof_hex", "[click](https://good.example&#x40;evil.example/p)"},
		{"escaped_backtick_fake_code_span", "\\`[click](javascript:alert(1))`"},
		{"code_span_trailing_backslash_closes", "`a\\` [click](javascript:alert(1))"},
		{"html_tag_backtick_attr_swallow", `<a title="` + "`" + `">[click](javascript:alert(1))` + "`"},
		{"html_tag_gt_in_quoted_attr_swallow", `<a title=">` + "`" + `">[click](javascript:alert(1))` + "`"},
		{"multiline_inline_html_comment_backtick", "x <!--\n`--> ![img](https://evil.example/t.png) `"},
		{"multiline_inline_html_tag_backtick", "x <a\ntitle=\"`\">[click](javascript:alert(1))`"},
		{"autolink_backtick_swallow", "<https://example.com/`>[click](javascript:alert(1))`"},
		{"email_autolink_backtick_swallow", "<a`b@example.com> [click](javascript:alert(1))`"},
		{"inline_link_dest_backtick_swallow", "[x](https://example.com/`b) [y](javascript:alert(1))`"},
		{"inline_link_title_backtick_swallow", "[x](https://example.com \"`\") `foo` [y](javascript:alert(1))`"},
		{"list_marker_fence_polarity_flip", "- ```\n  code\n  ```\n[click](javascript:alert(1))"},
		{"empty_list_item_blank_line_fence_flip", "-\n\n  ```\nx\n  ```\n[click](javascript:alert(1))"},
		{"ordered_list_marker_fence_polarity_flip", "1. ```\n   code\n   ```\n[click](javascript:alert(1))"},
		{"list_marker_html_comment_fence_smuggle", "- <!--\n  ```\n  -->\n  [click](javascript:alert(1))\n  ```"},
		{"list_marker_html_div_fence_smuggle", "- <div>\n  ```\n  </div>\n\n[click](javascript:alert(1))\n```"},
		{"type7_html_after_heading_fence_smuggle", "# heading\n<a title=\">\">\n```\n\n[click](javascript:alert(1))\n```"},
		{"type7_html_after_hr_fence_smuggle", "---\n<custom-el>\n```\n\n[click](javascript:alert(1))\n```"},
		{"ref_def_backtick_in_label_and_title", "[a`]: https://good.example@evil.example \"`\""},
		{"ref_def_multiline_across_ordered_2", "[foo\n2. bar]: javascript:alert(1)\n\n[click][foo 2. bar]"},
		{"ref_def_multiline_across_empty_dash", "[foo\n-]: javascript:alert(1)\n\n[click][foo -]"},
		{"angle_link_space_userinfo", "[click](<https://good.example @evil.example/p>)"},
		{"angle_link_tab_userinfo", "[click](<https://good.example\t@evil.example/p>)"},
		{"balanced_paren_link_userinfo", "[click](https://good.example(a)@evil.example/p)"},
		{"backslash_escaped_scheme_link", `[click](java\script:alert(1))`},
		{"backslash_escaped_userinfo_link", `[click](https://good.example\@evil.example/p)`},
		{"image_with_https_url", "![tracker](https://evil.example/t.png)"},
		{"gfm_bare_http_autolink", "Visit http://evil.example/p now"},
		{"gfm_bare_www_autolink", "Visit www.evil.example/p now"},
		{"gfm_bare_https_userinfo_phish", "Visit https://good.example@evil.example/p now"},
		{"gfm_table_bare_http", "|http://evil.example/p|"},
		{"gfm_table_bare_https_userinfo", "|https://good.example@evil.example/p|"},
		{"gfm_table_bare_www", "|www.evil.example/p|"},
		{"fake_link_title_hides_image", `](https://example.com "![img](https://evil.example/t.png)")`},
		{"nested_link_title_hides_image", `[a [b](https://example.com) c](https://example.com "![img](https://evil.example/t.png)")`},
		{"fake_link_title_hides_js_link", `](https://example.com "[click](javascript:alert(1))")`},
		{"invalid_decl_hides_js_link", `x <!a[click](javascript:alert(1))>`},
		{"commonmark029_double_dash_comment_hides_link", `x <!-- -- [click](javascript:alert(1)) -->`},
		{"commonmark029_triple_dash_comment_hides_link", `x <!-- [click](javascript:alert(1)) --->`},
		{"commonmark031_double_dash_comment_backtick_swallow", "x <!-- -- ` --> [click](https://good.example@evil.example/p) `"},
		{"commonmark031_lowercase_decl_backtick_swallow", "x <!a `> [click](https://good.example@evil.example/p) `"},
		{"tab_after_blockquote_fence", "> \t```\n> code\n> ```\n[click](javascript:alert(1))"},
		{"tab_after_blockquote_html_comment", "> \t<!--\n> ```\n> -->\n> [click](javascript:alert(1))\n> ```"},
		{"tab_after_blockquote_ref_def", "> \t[ref]: javascript:alert(1)\n\n[click][ref]"},
		{"tab_after_list_fence", "1. item\n   \t```\n   code\n   ```\n[click](javascript:alert(1))"},
		{"setext_heading_then_type7_html_fence_smuggle", "Heading\n===\n<span>\n```\n\n![img](https://evil.example/t.png)\n```"},
		{"sibling_ordered_list_fence_polarity", "1. foo\n2. ```\n   ```\n   ![img](https://evil.example/t.png)"},
		{"empty_list_nested_blockquote_fence", "-\n  > ```\n  > code\n  > ```\n  ![img](https://evil.example/t.png)"},
		{"empty_list_empty_blockquote_blank_line_fence", "-\n  >\n\n  ```\n![img](https://evil.example/t.png)"},
		{"html_block_blank_line_backtick_pair", "<!--\n`\n\n` ![img](https://evil.example/t.png)\n-->"},
		{"cjk_400_rune_1200_byte_ref_def", "[" + strings.Repeat("中", 400) + "]: javascript:alert(1)\n\n[x][" + strings.Repeat("中", 400) + "]"},
		{"deep_35_nested_paren_link_userinfo", "[click](https://good.example" + strings.Repeat("(", 35) + "a" + strings.Repeat(")", 35) + "@evil.example/p)"},
		{"deep_ordered_list_ref_def", "10. item\n\n    [ref]: javascript:alert(1)\n\n[click][ref]"},
		{"tilde_fence_short_close_then_link", "~~~\ncode\n~~~\n[click](javascript:alert(1))"},
	}
	for _, tc := range rejected {
		t.Run("reject_markdown/"+tc.name, func(t *testing.T) {
			v := &View{Sections: []Section{{Items: []Component{{Type: "markdown", Text: tc.text}}}}}
			if err := CheckView(v); err == nil {
				t.Errorf("CheckView(markdown) unexpectedly accepted %q", tc.text)
			}
		})
		t.Run("reject_comment/"+tc.name, func(t *testing.T) {
			v := &View{Sections: []Section{{Items: []Component{{
				Type:     "comment_thread",
				Comments: []Comment{{ID: "c1", Author: "a", Body: tc.text}},
			}}}}}
			if err := CheckView(v); err == nil {
				t.Errorf("CheckView(comment_thread) unexpectedly accepted %q", tc.text)
			}
		})
	}

	accepted := []struct {
		name string
		text string
	}{
		{"https_link_with_query_entity", "[docs](https://example.com/search?a=1&amp;b=2)"},
		{"autolink_https", "<https://example.com/path>"},
		{"gfm_bare_https_autolink", "See https://example.com/docs/v1. Also https://www.example.com/ok."},
		{"gfm_table_https_autolink", "|https://example.com/docs|"},
		{"bare_www_dot_no_panic", "www."},
		{"visit_www_dot_no_panic", "Visit www."},
		{"bare_www_ellipsis_no_panic", "www..."},
		{"bare_www_punct_paren_no_panic", "www.?)"},
		{"single_backtick_code_span", "`[click](javascript:alert(1))`"},
		{"double_backtick_code_span_with_backtick", "`` `![x][img]` ``"},
		{"triple_backtick_inline_span_closed", "note ```[click](javascript:alert(1))``` end"},
		{"column0_backtick_fence", "```js\n[click](javascript:alert(1))\n![img](https://example.com/x.png)\n```"},
		{"indented_toplevel_fence_with_col0_line", "  ```\n[click](javascript:alert(1))\n  ```"},
		{"column0_tilde_fence_with_backticks", "~~~a`\n[click](javascript:alert(1))\n~~~"},
		{"four_backtick_fence_with_three_inside", "````\n```\n[click](javascript:alert(1))\n````"},
		{"list_indented_fence_body_stays_indented", "- item\n  ```\n  [click](javascript:alert(1))\n  ```"},
		{"list_marker_prose_bracket_colon", "- [Fix]: Handle edge cases"},
		{"blockquote_prose_bracket_colon", "> [Note]: See details below"},
		{"plain_bracket_text_not_ref_def", "See [Note] details: all good."},
		{"inline_link_with_escaped_bracket_text", `[a\]](https://example.com)`},
		{"angle_comparison_around_code_span", "x < y && `![x][img]` > z"},
		{"html_anchor_https_href", `<a href="https://example.com">`},
		{"inline_link_title_with_https_url", `[x](https://example.com "see https://example.com")`},
	}
	for _, tc := range accepted {
		t.Run("accept_markdown/"+tc.name, func(t *testing.T) {
			v := &View{Sections: []Section{{Items: []Component{{Type: "markdown", Text: tc.text}}}}}
			if err := CheckView(v); err != nil {
				t.Errorf("CheckView(markdown) rejected valid %q: %v", tc.text, err)
			}
		})
	}
}

func TestCheckMarkdownLinearTimeNoDoS(t *testing.T) {
	payloads := []struct {
		name string
		text string
	}{
		{"unclosed_pi_20kb", strings.Repeat("<?", 9999) + "`"},
		{"unclosed_comment_20kb", strings.Repeat("<!--", 5000) + "`"},
		{"unclosed_cdata_20kb", strings.Repeat("<![CDATA[", 2200) + "`"},
		{"unclosed_quote_tag_20kb", strings.Repeat(`<a b="`, 3300) + "`"},
		{"unclosed_backticks_20kb", strings.Repeat("` `` ", 4000)},
		{"repeated_comment_double_dash_closed_20kb", strings.Repeat("<!-- -- ", 2500) + "-->"},
		{"repeated_decl_closed_20kb", strings.Repeat("<!A ", 5000) + ">"},
		{"repeated_pi_closed_20kb", strings.Repeat("<? ", 6000) + "?>"},
		{"gfm_trailing_parens_20kb", "https://example.com/" + strings.Repeat(")", 19980)},
		{"nested_list_tab_blank_lines_20kb", strings.Repeat("-\t", 1000) + "\n" + strings.Repeat(strings.Repeat("\t", 1000)+"\n", 17)},
	}
	for _, tc := range payloads {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			if err := checkMarkdown(tc.text); err != nil {
				t.Fatalf("unexpected error on %s: %v", tc.name, err)
			}
			if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
				t.Fatalf("%s took %v (expected linear time < 500ms)", tc.name, elapsed)
			}
		})
	}
}

func TestAssistantVersionSafeAcrossLanguages(t *testing.T) {
	for _, version := range []string{"9007199254740992", "9223372036854775808"} {
		raw := []byte(`{"id":"view_example","conversation":"example","version":` + version + `,"view":{"sections":[]},"created_at":"2026-11-05T00:00:00Z","updated_at":"2026-11-05T00:00:00Z"}`)
		if err := ValidateJSON("AssistantView", raw); err == nil {
			t.Fatalf("accepted unsafe version %s", version)
		}
	}
	raw := []byte(`{"id":"view_example","conversation":"example","version":9007199254740991,"view":{"sections":[]},"created_at":"2026-11-05T00:00:00Z","updated_at":"2026-11-05T00:00:00Z"}`)
	if err := ValidateJSON("AssistantView", raw); err != nil {
		t.Fatal(err)
	}
	var v AssistantView
	if err := json.Unmarshal(raw, &v); err != nil || v.Version != 9007199254740991 {
		t.Fatalf("%+v, %v", v, err)
	}
}

func TestAvatarURLRequiresSafeHTTPS(t *testing.T) {
	badURLs := []struct {
		name string
		url  string
	}{
		{"userinfo_credentials", "https://user:pass@evil.example/a.png"},
		{"userinfo_host_spoof", "https://github.com@evil.example/a.png"},
		{"userinfo_empty", "https://@evil.example/a.png"},
		{"backslash_host_spoof", `https://evil.example\@trusted.example/a.png`},
		{"scheme_relative_path", "https:/admin"},
		{"empty_host_port", "https://:443/a.png"},
		{"malformed_host", "https://[invalid/a.png"},
		{"crlf_control", "https://evil.example\r\nX: 1"},
		{"http_scheme", "http://evil.example/a.png"},
		{"javascript_scheme", "javascript:alert(1)"},
	}
	slots := []struct {
		name string
		item func(u string) Component
	}{
		{"row", func(u string) Component {
			return Component{Type: "row", Title: "r", Avatar: &Avatar{URL: u}}
		}},
		{"header", func(u string) Component {
			return Component{Type: "header", Title: "h", Avatar: &Avatar{URL: u}}
		}},
		{"comment_thread", func(u string) Component {
			return Component{Type: "comment_thread", Comments: []Comment{{ID: "c1", Author: "a", Body: "ok", Avatar: &Avatar{URL: u}}}}
		}},
		{"diff_thread_comment", func(u string) Component {
			return Component{Type: "diff", Path: "a.go", Hunks: []Hunk{{Header: "@@ -1 +1 @@", Lines: []string{"+x"}}}, Threads: []Thread{{Side: "new", Line: 1, Comments: []Comment{{ID: "c1", Author: "a", Body: "ok", Avatar: &Avatar{URL: u}}}}}}
		}},
	}
	for _, slot := range slots {
		for _, bad := range badURLs {
			t.Run(slot.name+"/"+bad.name, func(t *testing.T) {
				v := &View{Sections: []Section{{Items: []Component{slot.item(bad.url)}}}}
				if err := CheckView(v); err == nil {
					t.Errorf("CheckView accepted unsafe avatar URL %q in %s", bad.url, slot.name)
				}
				raw, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				if err := CheckViewJSON(raw); err == nil {
					t.Errorf("CheckViewJSON accepted unsafe avatar URL %q in %s", bad.url, slot.name)
				}
			})
		}
		t.Run(slot.name+"/valid_https", func(t *testing.T) {
			v := &View{Sections: []Section{{Items: []Component{slot.item("https://example.com/avatar.png")}}}}
			if err := CheckView(v); err != nil {
				t.Errorf("CheckView rejected valid avatar URL in %s: %v", slot.name, err)
			}
		})
	}
}

func TestCodeLineURLRequiresSafeHTTPS(t *testing.T) {
	badURLs := []struct {
		name string
		url  string
	}{
		{"userinfo_host_spoof", "https://github.com@evil.example/blob/main/a.go#L{start}-L{end}"},
		{"userinfo_credentials", "https://user:pass@evil.example/a.go#L{start}"},
		{"userinfo_empty", "https://@evil.example/a.go#L{start}"},
		{"backslash_host_spoof", `https://evil.example\@trusted.example/a.go#L{start}`},
		{"scheme_relative_path", "https:/admin#L{start}"},
		{"empty_host_port", "https://:443/a.go#L{start}"},
		{"malformed_host", "https://[invalid/a.go#L{start}"},
		{"crlf_control", "https://evil.example\r\n/a.go#L{start}"},
		{"http_scheme", "http://example.com/a.go#L{start}"},
	}
	for _, bad := range badURLs {
		t.Run("reject/"+bad.name, func(t *testing.T) {
			v := &View{Sections: []Section{{Items: []Component{{Type: "code", Text: "x", LineURL: bad.url}}}}}
			if err := CheckView(v); err == nil {
				t.Errorf("CheckView accepted unsafe line_url %q", bad.url)
			}
			raw, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			if err := CheckViewJSON(raw); err == nil {
				t.Errorf("CheckViewJSON accepted unsafe line_url %q", bad.url)
			}
		})
	}
	valid := &View{Sections: []Section{{Items: []Component{{Type: "code", Text: "x", LineURL: "https://example.com/org/repo/blob/main/a.go#L{start}-L{end}"}}}}}
	if err := CheckView(valid); err != nil {
		t.Errorf("CheckView rejected valid line_url: %v", err)
	}
}
