package parser

import "strings"

// RenderStep converts inline CookLang markup in a step to readable plain
// text:
//
//	@rice noodles{200%g}   -> rice noodles (200 g)
//	@peanuts(crushed)      -> peanuts (crushed)
//	@?thyme                -> thyme
//	#wok                   -> wok
//	~{25%minutes}          -> 25 minutes
//	\@literal              -> @literal
//
// Name rules follow cooklang-rs's comp_body: a name without braces is a
// single word (letters, digits, unicode); a multi-word name must be
// terminated by {} or ().
func RenderStep(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch c {
		case '\\': // escaped markup character
			if i+1 < len(s) && strings.ContainsRune("@#~\\", rune(s[i+1])) {
				b.WriteByte(s[i+1])
				i += 2
				continue
			}
			b.WriteByte(c)
			i++
		case '@', '#', '~':
			rest, consumed := renderNode(s[i:])
			b.WriteString(rest)
			i += consumed
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// renderNode renders the markup node at the start of s (which must begin
// with '@', '#' or '~') and returns the rendered text plus bytes consumed.
func renderNode(s string) (string, int) {
	kind := s[0]

	// Strip modifiers: '?' is core syntax (optional); '-+&@' are
	// cooklang-rs extensions we tolerate but don't model.
	j := 1
	for j < len(s) && strings.ContainsRune("?-+&@", rune(s[j])) {
		j++
	}
	mods := j // name starts after the modifiers

	// Bare name: a single word. Word bytes are anything but whitespace,
	// markup punctuation and sentence punctuation.
	isWordByte := func(c byte) bool {
		return !strings.ContainsRune("{}() \t,.;:!?'\"@#~", rune(c))
	}
	for j < len(s) && isWordByte(s[j]) {
		j++
	}
	// Multi-word names are valid when a '{' or '(' terminates the whole
	// run ("@olive oil{2%tbsp}"); absorb words only in that case.
	for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
		k := j + 1
		for k < len(s) && (isWordByte(s[k]) || s[k] == ' ') {
			k++
		}
		if k < len(s) && (s[k] == '{' || s[k] == '(') {
			j = k // the word run is part of the name
		} else {
			break
		}
	}
	name := strings.TrimSpace(s[mods:j])
	consumed := j

	var qty, unit, note string
	// optional {quantity%unit}
	if consumed < len(s) && s[consumed] == '{' {
		if end := strings.IndexByte(s[consumed:], '}'); end >= 0 {
			q := s[consumed+1 : consumed+end]
			if amt, u, ok := strings.Cut(q, "%"); ok {
				qty, unit = strings.TrimSpace(amt), strings.TrimSpace(u)
			} else {
				qty = strings.TrimSpace(q)
			}
			consumed += end + 1
		}
	}
	// optional (note)
	if consumed < len(s) && s[consumed] == '(' {
		if end := strings.IndexByte(s[consumed:], ')'); end >= 0 {
			note = strings.TrimSpace(s[consumed+1 : consumed+end])
			consumed += end + 1
		}
	}

	switch kind {
	case '~': // timer: ~name{qty%unit} -> "qty unit"
		var parts []string
		for _, p := range []string{qty, unit} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		if len(parts) == 0 {
			return name, consumed // degenerate timer; keep the name
		}
		return strings.Join(parts, " "), consumed
	case '#': // cookware: #wok -> "wok"
		return name, consumed
	default: // ingredient: @name{qty%unit}(note) -> "name (qty unit, note)"
		var paren []string
		if qty != "" {
			paren = append(paren, strings.TrimSpace(qty+" "+unit))
		}
		if note != "" {
			paren = append(paren, note)
		}
		if len(paren) == 0 {
			return name, consumed
		}
		return name + " (" + strings.Join(paren, ", ") + ")", consumed
	}
}

// parseIngredientLine parses an ingredient-list line such as
// "@?thyme{}", "@olive oil{200%g}" or "@peanuts(roughly crushed)",
// tolerating cooklang-rs modifier extensions.
func parseIngredientLine(line string) Ingredient {
	// Drop any trailing comment; notes for list lines are not kept.
	if idx := indexUnescaped(line, "--"); idx >= 0 {
		line = line[:idx]
	}
	if idx := indexUnescaped(line, "[-"); idx >= 0 {
		line = line[:idx]
	}

	s := strings.TrimPrefix(strings.TrimSpace(line), "@")
	ing := Ingredient{}

	// modifiers: '?' marks optional; '-+&@' are tolerated extensions
	for len(s) > 0 && strings.ContainsRune("?-+&@", rune(s[0])) {
		if s[0] == '?' {
			ing.Optional = true
		}
		s = s[1:]
	}

	// note "(...)" at the end
	if note, ok := cutLastParen(s); ok {
		ing.Note = note
		s = strings.TrimSpace(s[:strings.LastIndex(s, "(")])
	}

	// quantity "{amount%unit}"
	if open := strings.IndexByte(s, '{'); open >= 0 {
		if close := strings.LastIndexByte(s, '}'); close > open {
			ing.Name = strings.TrimSpace(s[:open])
			q := strings.TrimSpace(s[open+1 : close])
			switch {
			case q == "":
				ing.Amount = "some" // "@milk{}" per the spec
			default:
				if amt, unit, ok := strings.Cut(q, "%"); ok {
					if amt = strings.TrimSpace(amt); amt != "" {
						ing.Amount = amt
					} else {
						ing.Amount = "some" // "%cups" -> some cups
					}
					if u := strings.TrimSpace(unit); u != "" {
						ing.Unit = u
					}
				} else if amt, unit, ok := strings.Cut(q, " "); ok {
					// tolerate "@thing{250 g}" (space instead of %)
					ing.Amount = strings.TrimSpace(amt)
					ing.Unit = strings.TrimSpace(unit)
				} else {
					ing.Amount = q
				}
			}
		}
	}
	if ing.Name == "" {
		ing.Name = strings.TrimSpace(s)
	}
	return ing
}

// cutLastParen extracts the text of the last "(...)" group in s.
func cutLastParen(s string) (string, bool) {
	open := strings.LastIndexByte(s, '(')
	if open < 0 {
		return "", false
	}
	close := strings.LastIndexByte(s, ')')
	if close != len(s)-1 || close <= open {
		return "", false
	}
	note := strings.TrimSpace(s[open+1 : close])
	return note, note != ""
}
