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

// Recipe is the result of parsing a .cook file.
type Recipe struct {
	Title       string            `json:"title"`
	Tags        []string          `json:"tags"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Ingredients []Ingredient      `json:"ingredients"`
	Steps       []string          `json:"steps"`
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
		para = append(para, line)
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

	return r, nil
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
