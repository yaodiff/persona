package shellcmd

import (
	"reflect"
	"testing"
)

func TestSplit(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		want []Segment
		ok   bool
	}{
		{`A="x y" claude --model 'm 1' # note`, []Segment{{Words: []string{"A=x y", "claude", "--model", "m 1"}, Text: `A="x y" claude --model 'm 1' # note`}}, true},
		{"a \\\n  b; c && d | e", []Segment{
			{Words: []string{"a", "b"}, Text: "a \\\n  b"}, {Words: []string{"c"}, Text: "c"}, {Words: []string{"d"}, Text: "d"},
			{Words: []string{"e"}, Text: "e"},
		}, true},
		{`echo "$(id)" > /tmp/x 2>&1`, []Segment{{Words: []string{"echo", "$(id)"}, Text: `echo "$(id)" > /tmp/x 2>&1`, Subst: true,
			ArgSubst: true, Nested: []string{"id"}, Redirect: ">/tmp/x 2>&1"}}, true},
		// Non-POSIX Unicode whitespace (\v, \u00a0) is part of the redirect target filename, never stripped.
		{"x >a\v >b\u00a0", []Segment{{Words: []string{"x"}, Text: "x >a\v >b", Redirect: ">a\v >b\u00a0"}}, true},
		{`echo 'open`, nil, false},
		{`echo $(printf ")`, nil, false},
		{`(cd x)`, nil, false},
		{`cat <<EOF`, nil, false},
		{`echo $(printf ")") hi`, []Segment{{Words: []string{"echo", `$(printf ")")`, "hi"}, Text: `echo $(printf ")") hi`,
			Subst: true, ArgSubst: true, Nested: []string{`printf ")"`}}}, true},
		{`echo "$(printf ")")"`, []Segment{{Words: []string{"echo", `$(printf ")")`}, Text: `echo "$(printf ")")"`,
			Subst: true, ArgSubst: true, Nested: []string{`printf ")"`}}}, true},
		{`echo $(printf ")"; send-mail --to alice) # "`, []Segment{{Words: []string{"echo", `$(printf ")"; send-mail --to alice)`},
			Text: `echo $(printf ")"; send-mail --to alice) # "`, Subst: true, ArgSubst: true,
			Nested: []string{`printf ")"; send-mail --to alice`}}}, true},
		{`echo $(printf "%s" "$(send-mail)")`, []Segment{{Words: []string{"echo", `$(printf "%s" "$(send-mail)")`},
			Text: `echo $(printf "%s" "$(send-mail)")`, Subst: true, ArgSubst: true,
			Nested: []string{"send-mail", `printf "%s" "$(send-mail)"`}}}, true},
		{"echo $(\n# )\nsend-mail\n)", []Segment{{Words: []string{"echo", "$(\n# )\nsend-mail\n)"},
			Text: "echo $(\n# )\nsend-mail\n)", Subst: true, ArgSubst: true,
			Nested: []string{"\n# )\nsend-mail\n"}}}, true},
	} {
		got, ok := Split(c.cmd)
		if ok != c.ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %+v %v, want %+v %v", c.cmd, got, ok, c.want, c.ok)
		}
	}
}

func TestSplitRedirectQuotingAndShellHardening(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		want []Segment
		ok   bool
	}{
		// Quoted and escaped redirection targets must not desynchronize quote state or hide trailing commands.
		{
			`echo > "a; echo " ; cua-driver call click '{}' # "`,
			[]Segment{
				{Words: []string{"echo"}, Text: `echo > "a; echo "`, Redirect: `>"a; echo "`},
				{Words: []string{"cua-driver", "call", "click", "{}"}, Text: `cua-driver call click '{}' # "`},
			},
			true,
		},
		{
			`cat < 'a; echo ' ; lark-cli im +messages-send --text hi # '`,
			[]Segment{
				{Words: []string{"cat"}, Text: `cat < 'a; echo '`, Redirect: `<'a; echo '`},
				{Words: []string{"lark-cli", "im", "+messages-send", "--text", "hi"}, Text: `lark-cli im +messages-send --text hi # '`},
			},
			true,
		},
		{
			`echo > "a\"; echo " ; id # "`,
			[]Segment{
				{Words: []string{"echo"}, Text: `echo > "a\"; echo "`, Redirect: `>"a\"; echo "`},
				{Words: []string{"id"}, Text: `id # "`},
			},
			true,
		},
		{
			`echo > /tmp/a\ b; echo ok`,
			[]Segment{
				{Words: []string{"echo"}, Text: `echo > /tmp/a\ b`, Redirect: `>/tmp/a\ b`},
				{Words: []string{"echo", "ok"}, Text: `echo ok`},
			},
			true,
		},
		{
			`echo > /tmp/a\;b; id`,
			[]Segment{
				{Words: []string{"echo"}, Text: `echo > /tmp/a\;b`, Redirect: `>/tmp/a\;b`},
				{Words: []string{"id"}, Text: `id`},
			},
			true,
		},
		{
			"echo > \\\n\"a; echo \" ; id # \"",
			[]Segment{
				{Words: []string{"echo"}, Text: "echo > \\\n\"a; echo \"", Redirect: `>"a; echo "`},
				{Words: []string{"id"}, Text: `id # "`},
			},
			true,
		},
		// Command substitutions in redirect targets preserve target text and mark both Subst and ArgSubst.
		{
			`echo > $(id)`,
			[]Segment{
				{Words: []string{"echo"}, Text: `echo > $(id)`, Subst: true, ArgSubst: true, Nested: []string{"id"}, Redirect: `>$(id)`},
			},
			true,
		},
		{
			"echo > `id`",
			[]Segment{
				{Words: []string{"echo"}, Text: "echo > `id`", Subst: true, ArgSubst: true, Nested: []string{"id"}, Redirect: ">`id`"},
			},
			true,
		},
		{
			`echo > "$(id)"`,
			[]Segment{
				{Words: []string{"echo"}, Text: `echo > "$(id)"`, Subst: true, ArgSubst: true, Nested: []string{"id"}, Redirect: `>"$(id)"`},
			},
			true,
		},
		{
			`echo > "a$(printf ")")b" ; id`,
			[]Segment{
				{Words: []string{"echo"}, Text: `echo > "a$(printf ")")b"`, Subst: true, ArgSubst: true, Nested: []string{`printf ")"`}, Redirect: `>"a$(printf ")")b"`},
				{Words: []string{"id"}, Text: `id`},
			},
			true,
		},
		// POSIX clobber redirect >| (and across line continuation), while <| stays a pipe.
		{
			`cua-driver >|/dev/null call click '{}'`,
			[]Segment{
				{Words: []string{"cua-driver", "call", "click", "{}"}, Text: `cua-driver >|/dev/null call click '{}'`, Redirect: `>|/dev/null`},
			},
			true,
		},
		{
			"cua-driver 2>\\\n|/dev/null call click '{}'",
			[]Segment{
				{Words: []string{"cua-driver", "call", "click", "{}"}, Text: "cua-driver 2>\\\n|/dev/null call click '{}'", Redirect: `2>|/dev/null`},
			},
			true,
		},
		{
			`echo <|cat`,
			[]Segment{
				{Words: []string{"echo"}, Text: `echo <`, Redirect: `<`},
				{Words: []string{"cat"}, Text: `cat`},
			},
			true,
		},
		// Line continuation inside double quotes disappears completely (never injects '\n').
		{
			"cua-driver \"ca\\\nll\" click '{}'",
			[]Segment{
				{Words: []string{"cua-driver", "call", "click", "{}"}, Text: "cua-driver \"ca\\\nll\" click '{}'"},
			},
			true,
		},
		// Line continuation splitting $(...) inside double quotes or unquoted must still be detected as Subst.
		{
			"cat /mem/MEMORY.md \"$\\\n(id)\"",
			[]Segment{
				{Words: []string{"cat", "/mem/MEMORY.md", "$(id)"}, Text: "cat /mem/MEMORY.md \"$\\\n(id)\"", Subst: true, ArgSubst: true, Nested: []string{"id"}},
			},
			true,
		},
		{
			"cat /mem/MEMORY.md $\\\n(id)",
			[]Segment{
				{Words: []string{"cat", "/mem/MEMORY.md", "$(id)"}, Text: "cat /mem/MEMORY.md $\\\n(id)", Subst: true, ArgSubst: true, Nested: []string{"id"}},
			},
			true,
		},
		{
			"echo > \"$\\\n(id)\"",
			[]Segment{
				{Words: []string{"echo"}, Text: "echo > \"$\\\n(id)\"", Subst: true, ArgSubst: true, Nested: []string{"id"}, Redirect: `>"$(id)"`},
			},
			true,
		},
		// Line continuation splitting && / || / &>.
		{
			"a &\\\n& b |\\\n| c &\\\n>/dev/null",
			[]Segment{
				{Words: []string{"a"}, Text: "a"},
				{Words: []string{"b"}, Text: "b"},
				{Words: []string{"c"}, Text: "c &\\\n>/dev/null", Redirect: "&>/dev/null"},
			},
			true,
		},
		// Non-POSIX Unicode whitespace in redirect targets must not be stripped (cannot forge >&1).
		{
			"cat /mem/MEMORY.md >&1\u00a0",
			[]Segment{
				{Words: []string{"cat", "/mem/MEMORY.md"}, Text: "cat /mem/MEMORY.md >&1", Redirect: ">&1\u00a0"},
			},
			true,
		},
		// Quoted digits, empty quoted args, and digits before &> are words, not fd prefixes.
		{
			`echo '2'>/dev/null`,
			[]Segment{
				{Words: []string{"echo", "2"}, Text: `echo '2'>/dev/null`, Redirect: `>/dev/null`},
			},
			true,
		},
		{
			`echo "">/dev/null`,
			[]Segment{
				{Words: []string{"echo", ""}, Text: `echo "">/dev/null`, Redirect: `>/dev/null`},
			},
			true,
		},
		{
			`echo 2&>/dev/null`,
			[]Segment{
				{Words: []string{"echo", "2"}, Text: `echo 2&>/dev/null`, Redirect: `&>/dev/null`},
			},
			true,
		},
		// Escaped \$"..." and literal "$'..." / "$"..." inside double quotes stay valid.
		{
			`echo "$'literal'" "$"foo \$"bar"`,
			[]Segment{
				{Words: []string{"echo", "$'literal'", "$foo", "$bar"}, Text: `echo "$'literal'" "$"foo \$"bar"`},
			},
			true,
		},
		// Quoted "case" inside $(...) is a normal argument, not a case statement.
		{
			`echo $(grep "case" /tmp/f)`,
			[]Segment{
				{Words: []string{"echo", `$(grep "case" /tmp/f)`}, Text: `echo $(grep "case" /tmp/f)`, Subst: true, ArgSubst: true, Nested: []string{`grep "case" /tmp/f`}},
			},
			true,
		},
		// Simple ${...} parameter expansions (unquoted, double-quoted, redirect, and inside $(...)) stay valid.
		{
			`echo ${n} "${d}" ${?} "${V}" ${n:-0} ${OTHER:=/tmp} "${X[1]}" "${#X}" "${!X}" "/c/${F:=links}" "${s%% *}" > "${d}/out"`,
			[]Segment{
				{
					Words:    []string{"echo", "${n}", "${d}", "${?}", "${V}", "${n:-0}", "${OTHER:=/tmp}", "${X[1]}", "${#X}", "${!X}", "/c/${F:=links}", "${s%% *}"},
					Text:     `echo ${n} "${d}" ${?} "${V}" ${n:-0} ${OTHER:=/tmp} "${X[1]}" "${#X}" "${!X}" "/c/${F:=links}" "${s%% *}" > "${d}/out"`,
					Redirect: `>"${d}/out"`,
				},
			},
			true,
		},
		{
			"echo \"$\\\n{HOME}\" $(echo ${d} \"${n:-0}\") $(jq '{a:1}' f) $(echo a{1,2})",
			[]Segment{
				{
					Words:    []string{"echo", "${HOME}", `$(echo ${d} "${n:-0}")`, `$(jq '{a:1}' f)`, `$(echo a{1,2})`},
					Text:     "echo \"$\\\n{HOME}\" $(echo ${d} \"${n:-0}\") $(jq '{a:1}' f) $(echo a{1,2})",
					Subst:    true,
					ArgSubst: true,
					Nested:   []string{`echo ${d} "${n:-0}"`, `jq '{a:1}' f`, `echo a{1,2}`},
				},
			},
			true,
		},
		// Balanced safe single quotes inside double-quoted ${...} parse identically in sh/bash/zsh and stay splittable
		// so downstream gates (larkgate) inspect the command instead of falling back.
		{
			`S=""; ssh -l "${S:=lark''-cli im +messages-send}" host "$S"`,
			[]Segment{
				{Words: []string{"S="}, Text: `S=""`},
				{Words: []string{"ssh", "-l", "${S:=lark''-cli im +messages-send}", "host", "$S"}, Text: `ssh -l "${S:=lark''-cli im +messages-send}" host "$S"`},
			},
			true,
		},
		// Unclosed quotes / substitutions / trailing bare backslash fail closed.
		{`echo > "unclosed`, nil, false},
		{`echo > 'unclosed`, nil, false},
		{`echo > "$(unclosed`, nil, false},
		{`echo > $(unclosed`, nil, false},
		{`echo a\`, nil, false},
		{`echo > a\`, nil, false},
		// ANSI-C ($'...') and Bash locale ($"...") quoting fail closed (including across \<newline>).
		{`echo $'a\'' ; rm -rf / # '`, nil, false},
		{"echo $\\\n'a\\'' ; cua-driver call click '{}' # '", nil, false},
		{`echo > $'a\'; echo ' ; rm -rf / # '`, nil, false},
		{`echo $($'a\'') ; rm -rf / # '`, nil, false},
		{`$"cua-driver" call click '{}'`, nil, false},
		{`cua-driver $"call" click '{}'`, nil, false},
		{"$\\\n\"cua-driver\" call click '{}'", nil, false},
		{`echo > $"a; echo " ; id # "`, nil, false},
		{`echo $($"foo")`, nil, false},
		// Complex/nested/unclosed ${...} expansions fail closed across words, redirects, and $(...).
		{`echo "${x:-"'"}" ; cua-driver call click "{}" # '`, nil, false},
		{`echo > "${x:-"'"}" ; cua-driver call click "{}" # '`, nil, false},
		{`echo $(echo "${x:-"'"}") ; cua-driver call click "{}" # ' )`, nil, false},
		{"echo \"$\\\n{x:-\"'\"}\" ; cua-driver call click \"{}\" # '", nil, false},
		{"echo \"$(echo ${x:-)} # \" '\n)\" ; cua-driver call click \"{}\" # '", nil, false},
		{`echo ${x:-"'"}; cua-driver call click '{}'`, nil, false},
		{`echo ${x:-'a'}`, nil, false},
		{`echo "${x:-'}" ; cua-driver call click '{}' # '`, nil, false},
		{`echo "${x:-'}'}" ; cua-driver call click '{}'`, nil, false},
		{`echo "${x:-'a;b'}"`, nil, false},
		{`echo ${x:-a;b}`, nil, false},
		{`echo > ${x:-a b}`, nil, false},
		{`echo "${x:-$(id)}"`, nil, false},
		{`echo "${x:-${y}}"`, nil, false},
		{`echo "${(e)x}"`, nil, false},
		{`echo "${x`, nil, false},
		{`echo ${}`, nil, false},
		// Line-continuation-split <<, <(, >( fail closed.
		{"cat <\\\n<EOF", nil, false},
		{"cat <\\\n(id)", nil, false},
		{"echo >\\\n(id)", nil, false},
		// skipGroup fails closed on nested backticks, heredocs, process substitutions, case statements, and brace groups.
		{"echo $(echo `echo \\\\\"`) ; cua-driver call click '{}' # \" )", nil, false},
		{"echo $(echo \"`id`\")", nil, false},
		{"echo $(cat <<EOF\n) \"\nEOF\n); cua-driver call click '{}'", nil, false},
		{"echo $(cat <\\\n<EOF\n)\nEOF\n)", nil, false},
		{"echo $(cat <(id))", nil, false},
		{"echo $(tee >(id))", nil, false},
		{"echo $(case x in a)#\"\n;; esac); cua-driver call click '{}'", nil, false},
		{"echo $(ca\\\nse x in a) echo hi ;; esac)", nil, false},
		{"echo $({case x in a)#\"\n;; esac}); cua-driver call click '{}' # \"", nil, false},
		{"echo $({# ) \"\necho hi\n}); cua-driver call click '{}' # \"", nil, false},
		{"echo $(f(){case x in a)#\"\n;; esac}); cua-driver call click '{}' # \"", nil, false},
	} {
		got, ok := Split(c.cmd)
		if ok != c.ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %+v %v, want %+v %v", c.cmd, got, ok, c.want, c.ok)
		}
	}
}
