// Package parser implements a small CookLang (.cook) parser.
//
// It understands the subset of CookLang needed for storage and indexing:
//
//	>> title: Pad Thai          -- metadata (key: value), collected into a map
//	>> tags: thai, noodles
//	@rice noodles{200 g}        -- ingredient lines at the top of the file
//	@peanuts(roughly chopped)
//
//	Fry the noodles in a hot wok. -- anything after the blank line is steps
//	-- this is a comment, ignored
package parser

import (
	"fmt"
	"strings"
)

// Ingredient is a single parsed ingredient from the ingredients section.
type Ingredient struct {
	Name   string `json:"name"`
	Amount string `json:"amount,omitempty"`
	Unit   string `json:"unit,omitempty"`
	Note   string `json:"note,omitempty"`
}

// Times holds the cooking times extracted from metadata such as
// ">> time required: 30 mins" or ">> cook time: 20 mins".
type Times struct {
	Total string `json:"total,omitempty"` // ">> time required:" / ">> total time:"
	Prep  string `json:"prep,omitempty"`  // ">> prep time:" / ">> preparation time:"
	Cook  string `json:"cook,omitempty"`  // ">> cook time:" / ">> cooking time:"
}

// Recipe is the result of parsing a .cook file.
type Recipe struct {
	Title       string            `json:"title"`
	Tags        []string          `json:"tags"`
	Times       Times             `json:"times"`
	Servings    string            `json:"servings,omitempty"`   // ">> servings:" / ">> serves:"
	Provenance  string            `json:"provenance,omitempty"` // ">> source:" (book, family, URL…)
	Metadata    map[string]string `json:"metadata,omitempty"`
	Ingredients []Ingredient      `json:"ingredients"`
	Steps       []string          `json:"steps"`  // rendered readable; Source keeps the raw text
	Source      string            `json:"source"` // the original .cook content
}

// Parse parses raw .cook content. fallbackTitle is used when the file has no
// ">> title:" metadata (e.g. derived from the uploaded filename).
func Parse(src, fallbackTitle string) (*Recipe, error) {
	if strings.TrimSpace(src) == "" {
		return nil, fmt.Errorf("empty recipe source")
	}

	r := &Recipe{
		Metadata:    map[string]string{},
		Ingredients: []Ingredient{},
		Steps:       []string{},
		Source:      src,
	}

	// Strip block comments [- ... -] spanning one or more lines.
	src = stripBlockComments(src)

	lines := strings.Split(src, "\n")
	i := 0

	// 1. Leading metadata block: ">> key: value"
	for ; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		if !strings.HasPrefix(line, ">>") {
			break
		}
		kv := strings.TrimSpace(strings.TrimPrefix(line, ">>"))
		if key, val, ok := strings.Cut(kv, ":"); ok {
			r.Metadata[strings.TrimSpace(strings.ToLower(key))] = strings.TrimSpace(val)
		}
	}

	// 2. Ingredients section: consecutive "@..." lines.
	for ; i < len(lines); i++ {
		line := strings.TrimSpace(stripLineComment(lines[i]))
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "@") {
			break
		}
		r.Ingredients = append(r.Ingredients, parseIngredient(strings.TrimPrefix(line, "@")))
	}

	// 3. Everything remaining is the method, split into paragraphs.
	var para []string
	flush := func() {
		if len(para) > 0 {
			r.Steps = append(r.Steps, strings.Join(para, " "))
			para = nil
		}
	}
	for ; i < len(lines); i++ {
		line := strings.TrimSpace(stripLineComment(lines[i]))
		if line == "" {
			flush()
			continue
		}
		para = append(para, RenderStep(line))
	}
	flush()

	// 4. Pull title/tags out of metadata.
	r.Title = strings.TrimSpace(r.Metadata["title"])
	if r.Title == "" {
		r.Title = fallbackTitle
	}
	if r.Title == "" {
		r.Title = firstWords(r.Steps, 8) // last resort: opening words of the method
	}
	if r.Title == "" {
		return nil, fmt.Errorf("recipe has no title and no steps to derive one from")
	}

	for _, tag := range strings.Split(r.Metadata["tags"], ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			r.Tags = append(r.Tags, tag)
		}
	}

	// 5. Pull structured sub-text (times, servings, provenance) out of
	//    metadata. Unknown keys stay available in Metadata.
	r.Times.Total = firstMetadata(r.Metadata, "time required", "total time")
	r.Times.Prep = firstMetadata(r.Metadata, "prep time", "preparation time")
	r.Times.Cook = firstMetadata(r.Metadata, "cook time", "cooking time")
	r.Servings = firstMetadata(r.Metadata, "servings", "serves")
	r.Provenance = r.Metadata["source"]

	return r, nil
}

// firstMetadata returns the first non-empty value among the keys.
func firstMetadata(md map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(md[k]); v != "" {
			return v
		}
	}
	return ""
}

// RenderStep converts inline CookLang markup in a step to readable plain
// text:
//
//	@rice noodles{200%g}   -> rice noodles (200 g)
//	@peanuts(crushed)      -> peanuts (crushed)
//	#wok                   -> wok
//	~{25%minutes}          -> 25 minutes
//	\@literal              -> @literal
//
// Unknown constructs are passed through unchanged.
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

	// A bare name is a single word: multi-word names must be delimited by
	// {} or () per the CookLang spec, so a bare name stops at whitespace or
	// punctuation. Words are only absorbed into the name if a '{' or '('
	// terminates the whole run (e.g. "@olive oil{2%tbsp}").
	isWordByte := func(c byte) bool {
		return !strings.ContainsRune("{}() \t,.;:!?\"'", rune(c))
	}
	j := 1
	for j < len(s) && isWordByte(s[j]) {
		j++
	}
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
	name := strings.TrimSpace(s[1:j])
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

// parseIngredient parses the text after the leading '@' of an ingredient
// line. Supported forms (per the CookLang spec):
//
//	@name
//	@name{amount%unit}     unit optional: @name{2}
//	@name(some note)
//	@name{amount%unit}(note)
func parseIngredient(s string) Ingredient {
	ing := Ingredient{Name: s}

	if note, ok := cutLastParen(s); ok {
		ing.Note = note
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "("+note+")"))
		ing.Name = s
	}

	if open := strings.IndexByte(s, '{'); open >= 0 {
		if close := strings.LastIndexByte(s, '}'); close > open {
			ing.Name = strings.TrimSpace(s[:open])
			qty := s[open+1 : close]
			if amt, unit, ok := strings.Cut(qty, "%"); ok {
				// "@thing{2%cups}" or "@thing{2%}"
				ing.Amount = strings.TrimSpace(amt)
				ing.Unit = strings.TrimSpace(unit)
			} else if amt, unit, ok := strings.Cut(qty, " "); ok {
				// tolerate "@thing{200 g}" (space instead of %)
				ing.Amount = strings.TrimSpace(amt)
				ing.Unit = strings.TrimSpace(unit)
			} else {
				ing.Amount = strings.TrimSpace(qty)
			}
		}
	}

	ing.Name = strings.TrimSpace(ing.Name)
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

// stripLineComment removes a trailing "-- ..." comment from a line.
func stripLineComment(line string) string {
	if idx := strings.Index(line, "--"); idx >= 0 {
		return line[:idx]
	}
	return line
}

// stripBlockComments removes [- ... -] comments, which may span lines.
func stripBlockComments(src string) string {
	var b strings.Builder
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		switch {
		case inBlock:
			if strings.Contains(line, "-]") {
				line = line[strings.Index(line, "-]")+2:]
				inBlock = false
			} else {
				continue
			}
		}
		if strings.Contains(line, "[-") {
			start := strings.Index(line, "[-")
			end := strings.Index(line, "-]")
			if end > start {
				line = line[:start] + line[end+2:]
				// handle multiple block comments on one line, crudely
				for strings.Contains(line, "[-") && strings.Contains(line, "-]") {
					s, e := strings.Index(line, "[-"), strings.Index(line, "-]")
					if e <= s {
						break
					}
					line = line[:s] + line[e+2:]
				}
			} else {
				line = line[:start]
				inBlock = true
			}
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func firstWords(steps []string, n int) string {
	for _, s := range steps {
		words := strings.Fields(s)
		if len(words) == 0 {
			continue
		}
		if len(words) > n {
			words = append(words[:n:n], "...")
		}
		return strings.Join(words, " ")
	}
	return ""
}
