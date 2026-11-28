// Package shellcmd reads shell command lines the way a POSIX shell splits them, before it expands anything: nothing
// is expanded or run. The node's command gates and launcher lists and the manager's reading of a pasted configuration
// share it.
package shellcmd

import "strings"

// Segment is one simple command of a command line.
type Segment struct {
	Words    []string // quotes removed
	Text     string   // as written
	Subst    bool     // holds $(…), `…` or <(…): it runs something else too
	ArgSubst bool     // the substitution is in a command word or redirect target
	Nested   []string // command substitutions, classified separately without executing them
	Redirect string   // its redirections, e.g. "> /tmp/x 2>&1"
}

// skipLineCont advances past any POSIX line continuations (\ followed by \n).
func skipLineCont(r []rune, i int) int {
	for i+1 < len(r) && r[i] == '\\' && r[i+1] == '\n' {
		i += 2
	}
	return i
}

// scanParamExp scans a simple ${...} parameter expansion starting at '{' (index open) and returns the index of its
// matching '}'. Anything that can hold nested double quotes, escapes, parentheses, nested expansions, newlines,
// unbalanced single quotes (which bash and zsh parse differently inside "${...}"), or (when unquoted) shell
// control/redirection metacharacters fails closed. Balanced single quotes inside "${...}" containing only safe
// characters parse identically in sh, bash, and zsh.
func scanParamExp(r []rune, open int, unquoted bool) (int, bool) {
	inSingle := false
	for j := open + 1; j < len(r); j++ {
		c := r[j]
		if c == '\'' {
			if unquoted {
				return j, false
			}
			inSingle = !inSingle
			continue
		}
		if c == '}' {
			if inSingle || j == open+1 {
				return j, false
			}
			return j, true
		}
		if strings.ContainsRune("\"\\()`{$\n", c) {
			return j, false
		}
		if (unquoted || inSingle) && strings.ContainsRune(";|&<>", c) {
			return j, false
		}
		if unquoted && (c == ' ' || c == '\t') {
			return j, false
		}
	}
	return len(r), false
}

// Split splits a command line at |, ||, &&, ;, & and newlines outside quotes, each part into words. ok is false for
// what it does not follow (heredocs, subshells and groups, unbalanced quotes): a caller then decides nothing.
func Split(cmd string) (segs []Segment, ok bool) {
	r := []rune(cmd)
	var seg Segment
	var redirects []string // seg's, joined when it ends: one string grown per redirection would be quadratic
	var word strings.Builder
	inWord := false
	wordQuoted := false
	start := 0
	endWord := func() {
		if inWord {
			seg.Words = append(seg.Words, word.String())
			word.Reset()
			inWord = false
			wordQuoted = false
		}
	}
	endSeg := func(at int) {
		endWord()
		seg.Text = strings.TrimSpace(string(r[start:at]))
		seg.Redirect = strings.Join(redirects, " ")
		if len(seg.Words) > 0 || seg.Redirect != "" {
			segs = append(segs, seg)
		}
		seg, redirects = Segment{}, nil
		start = at
	}
	// skipGroup copies $( … ) / `…` into the word, nesting and quotes respected; false when it never closes or holds
	// syntax Split does not follow (heredocs, process substitution, nested backticks, case patterns, brace groups,
	// ANSI-C/locale quotes, or complex parameter expansions).
	var skipGroup func(i int, open, close rune) (int, bool)
	skipGroup = func(i int, open, close rune) (int, bool) {
		depth := 0
		begin := i + 1
		inGroupWord := false
		var tok strings.Builder
		tokQuoted := false
		finishTok := func() bool {
			if open == '(' && !tokQuoted && tok.String() == "case" {
				return false
			}
			tok.Reset()
			tokQuoted = false
			return true
		}
		for ; i < len(r); i++ {
			word.WriteRune(r[i])
			switch {
			case r[i] == '\\':
				if i+1 >= len(r) {
					return i, false
				}
				i++
				word.WriteRune(r[i])
				if r[i] != '\n' {
					inGroupWord = true
					tokQuoted = true
				}
			case r[i] == '$' && open != '`':
				next := skipLineCont(r, i+1)
				if next < len(r) {
					switch r[next] {
					case '\'', '"':
						return i, false // ANSI-C ($'...') or locale ($"...") quoting
					case '{':
						end, ok := scanParamExp(r, next, true)
						if !ok {
							return i, false
						}
						word.WriteString(string(r[i+1 : end+1]))
						inGroupWord = true
						tokQuoted = true
						i = end
						continue
					}
				}
				inGroupWord = true
				tok.WriteRune(r[i])
			case r[i] == '`' && open == '(':
				return i, false // nested backticks inside $(...)
			case r[i] == '<' && open == '(':
				if !finishTok() {
					return i, false
				}
				if next := skipLineCont(r, i+1); next < len(r) && (r[next] == '<' || r[next] == '(') {
					return i, false // heredoc / herestring / process substitution
				}
				inGroupWord = false
			case r[i] == '>' && open == '(':
				if !finishTok() {
					return i, false
				}
				if next := skipLineCont(r, i+1); next < len(r) && r[next] == '(' {
					return i, false // process substitution
				}
				inGroupWord = false
			case (r[i] == '{' || r[i] == '}') && open == '(' && !inGroupWord:
				return i, false // brace groups inside $(...) (including zsh spaceless {case / {#)
			case r[i] == '\'' && open != '`':
				inGroupWord = true
				tokQuoted = true
				for i++; i < len(r) && r[i] != '\''; i++ {
					word.WriteRune(r[i])
				}
				if i >= len(r) {
					return i, false
				}
				word.WriteRune(r[i])
			case r[i] == '"' && open != '`':
				inGroupWord = true
				tokQuoted = true
				for i++; i < len(r) && r[i] != '"'; i++ {
					switch {
					case r[i] == '\\' && i+1 < len(r) && strings.ContainsRune("\"\\$`\n", r[i+1]):
						word.WriteRune(r[i])
						i++
						word.WriteRune(r[i])
					case r[i] == '`':
						return i, false // nested backticks inside $(...)
					case r[i] == '$' && skipLineCont(r, i+1) < len(r) && r[skipLineCont(r, i+1)] == '(':
						next := skipLineCont(r, i+1)
						word.WriteString(string(r[i:next]))
						end, closed := skipGroup(next, '(', ')')
						if !closed {
							return end, false
						}
						i = end
					case r[i] == '$' && skipLineCont(r, i+1) < len(r) && r[skipLineCont(r, i+1)] == '{':
						next := skipLineCont(r, i+1)
						end, ok := scanParamExp(r, next, false)
						if !ok {
							return i, false
						}
						word.WriteString(string(r[i : end+1]))
						i = end
					default:
						word.WriteRune(r[i])
					}
				}
				if i >= len(r) {
					return i, false
				}
				word.WriteRune(r[i])
			case r[i] == '#' && open == '(' && !inGroupWord && depth > 0:
				for i+1 < len(r) && r[i+1] != '\n' {
					i++
					word.WriteRune(r[i])
				}
			case r[i] == open && (open != close || depth == 0):
				if !finishTok() {
					return i, false
				}
				depth++
				inGroupWord = false
			case r[i] == close:
				if !finishTok() {
					return i, false
				}
				depth--
				if depth == 0 {
					seg.Nested = append(seg.Nested, string(r[begin:i]))
					return i, true
				}
				inGroupWord = false
			case strings.ContainsRune(" \t\n;|&<>", r[i]):
				if !finishTok() {
					return i, false
				}
				inGroupWord = false
			default:
				inGroupWord = true
				tok.WriteRune(r[i])
			}
		}
		return i, false
	}
	for i := 0; i < len(r); i++ {
		c := r[i]
		next := skipLineCont(r, i+1)
		switch {
		case c == '\\':
			if i+1 >= len(r) {
				return nil, false // trailing bare backslash
			}
			i++
			if r[i] != '\n' { // a line continuation joins the lines
				word.WriteRune(r[i])
				inWord = true
				wordQuoted = true
			}
		case c == '\'':
			inWord = true
			wordQuoted = true
			j := i + 1
			for j < len(r) && r[j] != '\'' {
				j++
			}
			if j >= len(r) {
				return nil, false
			}
			word.WriteString(string(r[i+1 : j]))
			i = j
		case c == '"':
			inWord = true
			wordQuoted = true
			j := i + 1
			for ; j < len(r) && r[j] != '"'; j++ {
				switch {
				case r[j] == '\\' && j+1 < len(r) && r[j+1] == '\n':
					j++ // POSIX line continuation inside double quotes disappears
				case r[j] == '\\' && j+1 < len(r) && strings.ContainsRune("\"\\$`", r[j+1]):
					j++
					word.WriteRune(r[j])
				case r[j] == '$' && skipLineCont(r, j+1) < len(r) && r[skipLineCont(r, j+1)] == '(':
					seg.Subst, seg.ArgSubst = true, true
					word.WriteRune('$')
					end, closed := skipGroup(skipLineCont(r, j+1), '(', ')')
					if !closed {
						return nil, false
					}
					j = end
				case r[j] == '$' && skipLineCont(r, j+1) < len(r) && r[skipLineCont(r, j+1)] == '{':
					open := skipLineCont(r, j+1)
					end, ok := scanParamExp(r, open, false)
					if !ok {
						return nil, false
					}
					word.WriteRune('$')
					word.WriteString(string(r[open : end+1]))
					j = end
				case r[j] == '`':
					seg.Subst, seg.ArgSubst = true, true
					end, closed := skipGroup(j, '`', '`')
					if !closed {
						return nil, false
					}
					j = end
				default:
					word.WriteRune(r[j])
				}
			}
			if j >= len(r) {
				return nil, false
			}
			i = j
		case c == '$' && next < len(r) && (r[next] == '\'' || r[next] == '"'):
			return nil, false // ANSI-C ($'...') or locale ($"...") quoting
		case c == '$' && next < len(r) && r[next] == '(':
			seg.Subst, seg.ArgSubst, inWord = true, true, true
			word.WriteRune('$')
			end, closed := skipGroup(next, '(', ')')
			if !closed {
				return nil, false
			}
			i = end
		case c == '$' && next < len(r) && r[next] == '{':
			end, ok := scanParamExp(r, next, true)
			if !ok {
				return nil, false
			}
			word.WriteRune('$')
			word.WriteString(string(r[next : end+1]))
			inWord = true
			i = end
		case c == '`':
			seg.Subst, seg.ArgSubst, inWord = true, true, true
			end, closed := skipGroup(i, '`', '`')
			if !closed {
				return nil, false
			}
			i = end
		case (c == '<' || c == '>') && next < len(r) && r[next] == '(':
			return nil, false // process substitution
		case c == '<' && next < len(r) && r[next] == '<':
			return nil, false // heredoc / herestring
		case c == '>' || c == '<' || (c == '&' && next < len(r) && r[next] == '>'):
			// A redirection: an unquoted fd number just before < or > belongs to it; then its operator and target.
			var op strings.Builder // one string grown a character at a time would be quadratic
			if c != '&' && inWord && !wordQuoted && word.Len() > 0 && strings.Trim(word.String(), "0123456789") == "" {
				op.WriteString(word.String())
				word.Reset()
				inWord = false
				wordQuoted = false
			}
			endWord()
			op.WriteRune(r[i])
			i = skipLineCont(r, i+1)
			switch c {
			case '>':
				if i < len(r) && (r[i] == '>' || r[i] == '|' || r[i] == '&') {
					op.WriteRune(r[i])
					i = skipLineCont(r, i+1)
				}
			case '<':
				if i < len(r) && (r[i] == '>' || r[i] == '&') {
					op.WriteRune(r[i])
					i = skipLineCont(r, i+1)
				}
			case '&':
				op.WriteRune(r[i]) // '>'
				i = skipLineCont(r, i+1)
				if i < len(r) && r[i] == '>' {
					op.WriteRune(r[i])
					i = skipLineCont(r, i+1)
				}
			}
			for i < len(r) {
				if r[i] == ' ' || r[i] == '\t' {
					i++
				} else if r[i] == '\\' && i+1 < len(r) && r[i+1] == '\n' {
					i += 2
				} else {
					break
				}
			}
			for i < len(r) && !strings.ContainsRune(" \t\n|;&<>()", r[i]) {
				switch {
				case r[i] == '\\':
					if i+1 >= len(r) {
						return nil, false // trailing bare backslash
					}
					if r[i+1] == '\n' {
						i += 2
						continue
					}
					op.WriteRune(r[i])
					op.WriteRune(r[i+1])
					i += 2
				case r[i] == '\'':
					op.WriteRune(r[i])
					for i++; i < len(r) && r[i] != '\''; i++ {
						op.WriteRune(r[i])
					}
					if i >= len(r) {
						return nil, false
					}
					op.WriteRune(r[i])
					i++
				case r[i] == '"':
					op.WriteRune(r[i])
					for i++; i < len(r) && r[i] != '"'; i++ {
						switch {
						case r[i] == '\\' && i+1 < len(r) && r[i+1] == '\n':
							i++ // discard POSIX line continuation inside double quotes
						case r[i] == '\\' && i+1 < len(r) && strings.ContainsRune("\"\\$`", r[i+1]):
							op.WriteRune(r[i])
							i++
							op.WriteRune(r[i])
						case r[i] == '$' && skipLineCont(r, i+1) < len(r) && r[skipLineCont(r, i+1)] == '(':
							seg.Subst, seg.ArgSubst = true, true
							word.WriteRune('$')
							end, closed := skipGroup(skipLineCont(r, i+1), '(', ')')
							if !closed {
								return nil, false
							}
							op.WriteString(word.String())
							word.Reset()
							inWord = false
							wordQuoted = false
							i = end
						case r[i] == '$' && skipLineCont(r, i+1) < len(r) && r[skipLineCont(r, i+1)] == '{':
							open := skipLineCont(r, i+1)
							end, ok := scanParamExp(r, open, false)
							if !ok {
								return nil, false
							}
							op.WriteRune('$')
							op.WriteString(string(r[open : end+1]))
							i = end
						case r[i] == '`':
							seg.Subst, seg.ArgSubst = true, true
							end, closed := skipGroup(i, '`', '`')
							if !closed {
								return nil, false
							}
							op.WriteString(word.String())
							word.Reset()
							inWord = false
							wordQuoted = false
							i = end
						default:
							op.WriteRune(r[i])
						}
					}
					if i >= len(r) {
						return nil, false
					}
					op.WriteRune(r[i])
					i++
				case r[i] == '$' && skipLineCont(r, i+1) < len(r) && (r[skipLineCont(r, i+1)] == '\'' || r[skipLineCont(r, i+1)] == '"'):
					return nil, false // ANSI-C ($'...') or locale ($"...") quoting
				case r[i] == '$' && skipLineCont(r, i+1) < len(r) && r[skipLineCont(r, i+1)] == '(':
					seg.Subst, seg.ArgSubst = true, true
					word.WriteRune('$')
					end, closed := skipGroup(skipLineCont(r, i+1), '(', ')')
					if !closed {
						return nil, false
					}
					op.WriteString(word.String())
					word.Reset()
					inWord = false
					wordQuoted = false
					i = end + 1
				case r[i] == '$' && skipLineCont(r, i+1) < len(r) && r[skipLineCont(r, i+1)] == '{':
					open := skipLineCont(r, i+1)
					end, ok := scanParamExp(r, open, true)
					if !ok {
						return nil, false
					}
					op.WriteRune('$')
					op.WriteString(string(r[open : end+1]))
					i = end + 1
				case r[i] == '`':
					seg.Subst, seg.ArgSubst = true, true
					end, closed := skipGroup(i, '`', '`')
					if !closed {
						return nil, false
					}
					op.WriteString(word.String())
					word.Reset()
					inWord = false
					wordQuoted = false
					i = end + 1
				default:
					op.WriteRune(r[i])
					i++
				}
			}
			i--
			redirects = append(redirects, op.String())
		case c == '|' || c == ';' || c == '&' || c == '\n':
			endSeg(i)
			if next < len(r) && (c == '|' && r[next] == '|' || c == '&' && r[next] == '&') {
				i = next
			}
			start = i + 1
		case c == '(' || c == ')' || (c == '{' || c == '}') && !inWord:
			return nil, false // subshells and groups
		case c == '#' && !inWord:
			for i < len(r) && r[i] != '\n' {
				i++
			}
			i--
		case c == ' ' || c == '\t':
			endWord()
		default:
			word.WriteRune(c)
			inWord = true
		}
	}
	endSeg(len(r))
	return segs, true
}
