// Package parser implements a CookLang (.cook) parser modelled on the
// reference implementation, cooklang-rs (https://github.com/cooklang/cooklang-rs).
//
// Supported syntax:
//
//	>> title: Pad Thai          -- metadata (key: value), with indented
//	>>   continued value          multi-line values
//	>> servings: 4
//	@rice noodles{200%g}        -- ingredient list lines at the top
//	@?thyme{}                   -- optional ingredient (core spec modifier)
//
//	== Sauce ==                 -- section header
//	> a plain text paragraph    -- text block (not a numbered step)
//	Fry the @garlic{3%cloves} in a #wok( Large notes (line comments) and
//	[- block comments -] are kept as notes on the step they appear in.
package parser

import (
	"fmt"
	"strings"
)

// Ingredient is a single parsed ingredient from the ingredients section.
type Ingredient struct {
	Name     string `json:"name"`
	Amount   string `json:"amount,omitempty"`
	Unit     string `json:"unit,omitempty"`
	Note     string `json:"note,omitempty"`
	Optional bool   `json:"optional,omitempty"` // "@?name" per the core spec
}

// Step is one block of the method: the lines between blank lines.
type Step struct {
	// Section is the "== Name ==" header this step appears under, if any.
	// Only the first step of a section carries the name.
	Section string `json:"section,omitempty"`
	// Text is the step rendered to readable prose ("@garlic{3%cloves}"
	// becomes "garlic (3 cloves)").
	Text string `json:"text"`
	// Notes are the cook's notes (line "--" and block "[- -]" comments)
	// found in this step, in order.
	Notes []string `json:"notes,omitempty"`
	// TextBlock marks a "> paragraph": plain prose, not an instruction.
	TextBlock bool `json:"text_block,omitempty"`
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
	Steps       []Step            `json:"steps"`
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
		Steps:       []Step{},
		Source:      src,
	}

	lines := strings.Split(src, "\n")
	i := 0

	// 1. Leading metadata block: ">> key: value", values may continue on
	//    indented lines.
	i = r.parseMetadata(lines, i)

	// 2. Ingredients section: consecutive lines that are only components
	//    (an "@ingredient" line, possibly with a trailing comment).
	for ; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		if !strings.HasPrefix(line, "@") {
			break
		}
		r.Ingredients = append(r.Ingredients, parseIngredientLine(line))
	}

	// 3. Everything remaining is the method: blocks separated by blank
	//    lines. Each block is a step (or a section header, or a text block).
	section := ""
	for i < len(lines) {
		// Skip blank lines between blocks.
		for ; i < len(lines) && strings.TrimSpace(lines[i]) == ""; i++ {
		}
		if i >= len(lines) {
			break
		}

		// Collect the block: consecutive non-blank lines.
		block := []string{}
		for ; i < len(lines) && strings.TrimSpace(lines[i]) != ""; i++ {
			block = append(block, lines[i])
		}

		if name, ok := parseSectionHeader(block); ok {
			section = name
			continue
		}

		r.Steps = append(r.Steps, parseStepBlock(block, section))
		section = "" // only the first step after a header carries it
	}

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

	// 5. Structured sub-text (times, servings, provenance); unknown keys
	//    stay available in Metadata.
	r.Times.Total = firstMetadata(r.Metadata, "time required", "total time")
	r.Times.Prep = firstMetadata(r.Metadata, "prep time", "preparation time")
	r.Times.Cook = firstMetadata(r.Metadata, "cook time", "cooking time")
	r.Servings = firstMetadata(r.Metadata, "servings", "serves")
	r.Provenance = r.Metadata["source"]

	return r, nil
}

// parseMetadata consumes ">> key: value" lines from the start of lines
// (skipping blanks and comments). Values continue onto indented lines.
// Returns the index of the first unconsumed line.
func (r *Recipe) parseMetadata(lines []string, i int) int {
	for ; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		if !strings.HasPrefix(trimmed, ">>") {
			return i
		}
		kv := strings.TrimSpace(strings.TrimPrefix(trimmed, ">>"))
		key, val, ok := strings.Cut(kv, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.ToLower(key))
		val = strings.TrimSpace(val)

		// Multi-line value: continuation lines start with whitespace.
		for i+1 < len(lines) {
			next := lines[i+1]
			if strings.TrimSpace(next) == "" || (next[0] != ' ' && next[0] != '\t') {
				break
			}
			cont := strings.TrimSpace(next)
			if strings.HasPrefix(cont, ">>") || strings.HasPrefix(cont, "@") {
				break // looks like the next structure, not a continuation
			}
			val += " " + cont
			i++
		}
		r.Metadata[key] = val
	}
	return i
}

// parseSectionHeader reports whether the block is a section header
// ("== Name ==", "= Name") and returns the trimmed name. Following the
// cooklang-rs grammar: '='+, then the name, then optional trailing '='s;
// anything after the trailing '='s (other than whitespace/comments) makes
// it a step instead.
func parseSectionHeader(block []string) (string, bool) {
	line := strings.TrimSpace(stripTrailingComments(strings.TrimSpace(block[0])))
	if !strings.HasPrefix(line, "=") || strings.TrimLeft(line, "=") == "" {
		return "", false
	}
	s := strings.TrimLeft(line, "=")
	// name runs until the first '='; the rest is a run of '='s that must
	// be followed by nothing (cooklang-rs: `=+ name =* $`)
	name, rest, _ := strings.Cut(s, "=")
	trailing := strings.TrimSpace(strings.TrimLeft(rest, "="))
	if trailing != "" || len(block) > 1 {
		return "", false // content after the closing '=' -> it's a step
	}
	// a nameless header ("===") is still a section, just unnamed
	return strings.TrimSpace(name), true
}

// parseStepBlock renders one block of lines into a Step.
func parseStepBlock(block []string, section string) Step {
	step := Step{Section: section}

	textBlock := false
	if strings.HasPrefix(strings.TrimSpace(block[0]), ">") {
		textBlock = true
		block[0] = strings.TrimSpace(block[0])[1:]
	}

	var text strings.Builder
	inComment := false // inside an unclosed "[- ... -]" spanning lines
	for _, raw := range block {
		line := raw
		if inComment {
			// the whole line belongs to the open block comment
			if close := indexUnescaped(line, "-]"); close >= 0 {
				appendToLastNote(&step, line[:close])
				line = line[close+2:]
				inComment = false
			} else {
				appendToLastNote(&step, line)
				continue
			}
		}
		rest, unclosed := extractComments(&step, line)
		inComment = unclosed
		if t := strings.TrimSpace(rest); t != "" {
			if text.Len() > 0 {
				text.WriteByte(' ')
			}
			text.WriteString(RenderStep(t))
		}
	}
	step.Text = strings.TrimSpace(text.String())
	step.TextBlock = textBlock
	return step
}

// extractComments walks a line left-to-right, appending "--" line
// comments and "[- -]" block comments to the step's notes and returning
// the remaining text. An unclosed block comment is reported so the caller
// can treat following lines as comment text.
func extractComments(step *Step, line string) (rest string, unclosed bool) {
	var b strings.Builder
	for i := 0; i < len(line); {
		c := line[i]
		if c == '\\' && i+1 < len(line) {
			b.WriteByte(c)
			b.WriteByte(line[i+1])
			i += 2
			continue
		}
		if strings.HasPrefix(line[i:], "--") {
			if note := strings.TrimSpace(line[i+2:]); note != "" {
				step.Notes = append(step.Notes, note)
			}
			return b.String(), false // the rest of the line is comment
		}
		if strings.HasPrefix(line[i:], "[-") {
			if close := indexUnescaped(line[i+2:], "-]"); close >= 0 {
				if note := strings.TrimSpace(line[i+2 : i+2+close]); note != "" {
					step.Notes = append(step.Notes, note)
				}
				i += 2 + close + 2
				continue
			}
			// unclosed: comment runs on until a later line closes it
			if note := strings.TrimSpace(line[i+2:]); note != "" {
				step.Notes = append(step.Notes, note)
			}
			return b.String(), true
		}
		b.WriteByte(c)
		i++
	}
	return b.String(), false
}

// appendToLastNote appends text to the step's most recent note (used for
// multi-line block comments).
func appendToLastNote(step *Step, text string) {
	if len(step.Notes) == 0 {
		step.Notes = append(step.Notes, strings.TrimSpace(text))
		return
	}
	step.Notes[len(step.Notes)-1] = strings.TrimSpace(
		step.Notes[len(step.Notes)-1] + " " + strings.TrimSpace(text))
}

// indexUnescaped finds the first occurrence of sub in s that is not
// preceded by a backslash.
func indexUnescaped(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i] == '\\' {
			i++ // skip the escaped character
			continue
		}
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
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

func firstWords(steps []Step, n int) string {
	for _, s := range steps {
		words := strings.Fields(s.Text)
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

// stripTrailingComments removes trailing "--" and "[- -]" comments from a
// single line (used where notes are not collected, e.g. section headers).
func stripTrailingComments(line string) string {
	for {
		if idx := indexUnescaped(line, "--"); idx >= 0 {
			line = line[:idx]
		} else if open := indexUnescaped(line, "[-"); open >= 0 {
			line = line[:open]
		} else {
			return strings.TrimSpace(line)
		}
	}
}
