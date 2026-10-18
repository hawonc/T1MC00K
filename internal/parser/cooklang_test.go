package parser

import (
	"strings"
	"testing"
)

const padThai = `>> title: Pad Thai
>> tags: thai, noodles, stir-fry
>> time required: 30 mins

@rice noodles{200%g}
@chicken breast{250 g}(sliced thin)
@eggs{2}
@peanuts(roughly crushed)

Soak the noodles. -- keep them firm
[- The noodles should be bendy but still firm -]
Fry everything in a wok for ~{2%minutes}.

Serve hot.
`

func TestParse(t *testing.T) {
	r, err := Parse(padThai, "fallback")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if r.Title != "Pad Thai" {
		t.Errorf("title = %q, want %q", r.Title, "Pad Thai")
	}
	if want := []string{"thai", "noodles", "stir-fry"}; strings.Join(r.Tags, ",") != strings.Join(want, ",") {
		t.Errorf("tags = %v, want %v", r.Tags, want)
	}
	if r.Metadata["time required"] != "30 mins" {
		t.Errorf("metadata time required = %q", r.Metadata["time required"])
	}

	// structured sub-text
	if r.Times.Total != "30 mins" {
		t.Errorf("times.total = %q, want %q", r.Times.Total, "30 mins")
	}
	if r.Times.Prep != "" || r.Times.Cook != "" {
		t.Errorf("unexpected extra times: %+v", r.Times)
	}

	if len(r.Ingredients) != 4 {
		t.Fatalf("got %d ingredients, want 4: %+v", len(r.Ingredients), r.Ingredients)
	}
	cases := []Ingredient{
		{Name: "rice noodles", Amount: "200", Unit: "g"},
		{Name: "chicken breast", Amount: "250", Unit: "g", Note: "sliced thin"},
		{Name: "eggs", Amount: "2"},
		{Name: "peanuts", Note: "roughly crushed"},
	}
	for i, want := range cases {
		if got := r.Ingredients[i]; got != want {
			t.Errorf("ingredient[%d] = %+v, want %+v", i, got, want)
		}
	}

	// Two blocks: the comment lines belong to the first block and must
	// surface as notes on that step, not as text.
	if len(r.Steps) != 2 {
		t.Fatalf("got %d steps, want 2: %+v", len(r.Steps), r.Steps)
	}
	first := r.Steps[0]
	if !strings.Contains(first.Text, "Soak the noodles.") ||
		!strings.Contains(first.Text, "2 minutes") {
		t.Errorf("first step text wrong: %q", first.Text)
	}
	if strings.Contains(first.Text, "keep them firm") || strings.Contains(first.Text, "bendy") {
		t.Errorf("note text leaked into step text: %q", first.Text)
	}
	wantNotes := []string{"keep them firm", "The noodles should be bendy but still firm"}
	if strings.Join(first.Notes, "|") != strings.Join(wantNotes, "|") {
		t.Errorf("notes = %v, want %v", first.Notes, wantNotes)
	}
	if r.Steps[1].Text != "Serve hot." || len(r.Steps[1].Notes) != 0 {
		t.Errorf("second step wrong: %+v", r.Steps[1])
	}
}

func TestRenderStep(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Add @salt and pepper to taste.", "Add salt and pepper to taste."},
		{"Add @olive oil{2%tbsp} and stir.", "Add olive oil (2 tbsp) and stir."},
		{"Top with @peanuts(crushed) and @lime wedges.", "Top with peanuts (crushed) and lime wedges."},
		{"@ground black pepper{1%tsp} to finish", "ground black pepper (1 tsp) to finish"},
		{"Fry in a #wok until golden.", "Fry in a wok until golden."},
		{"Simmer for ~{25%minutes}.", "Simmer for 25 minutes."},
		{"Bake for ~cake{40%minutes}.", "Bake for 40 minutes."},
		{"Now add the @?thyme.", "Now add the thyme."},
		{"Email me at \\@not-markup.", "Email me at @not-markup."},
		{"Plain text stays put.", "Plain text stays put."},
	}
	for _, c := range cases {
		if got := RenderStep(c.in); got != c.want {
			t.Errorf("RenderStep(%q)\n  = %q\n  want %q", c.in, got, c.want)
		}
	}
}

func TestSectionsAndTextBlocks(t *testing.T) {
	src := `>> title: X

@flour{200%g}

== Sauce ==

Mix the sauce.

> Let it rest for a bit, it tastes better.

== Assembly ==

Assemble and serve.
`
	r, err := Parse(src, "")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(r.Steps) != 3 {
		t.Fatalf("got %d steps, want 3: %+v", len(r.Steps), r.Steps)
	}
	if r.Steps[0].Section != "Sauce" {
		t.Errorf("step 0 section = %q", r.Steps[0].Section)
	}
	if !r.Steps[1].TextBlock || r.Steps[1].Text != "Let it rest for a bit, it tastes better." {
		t.Errorf("text block wrong: %+v", r.Steps[1])
	}
	if r.Steps[2].Section != "Assembly" {
		t.Errorf("step 2 section = %q", r.Steps[2].Section)
	}
}

func TestSectionNeedsToEndOfLine(t *testing.T) {
	// content after the closing '=' makes it a step, not a section
	r, err := Parse("@flour{1%kg}\n\n== header == and more text\n", "")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if r.Steps[0].Section != "" {
		t.Errorf("expected a step, got section %q", r.Steps[0].Section)
	}
}

func TestMultilineMetadata(t *testing.T) {
	src := `>> title: X
>> source: an incredibly long provenance
          spanning two lines
>> servings: 4

@flour{200%g}

Mix.
`
	r, err := Parse(src, "")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if r.Provenance != "an incredibly long provenance spanning two lines" {
		t.Errorf("provenance = %q", r.Provenance)
	}
	if r.Servings != "4" {
		t.Errorf("servings = %q", r.Servings)
	}
}

func TestTimesAndSubtext(t *testing.T) {
	src := `>> title: X
>> prep time: 10 mins
>> cook time: 45 mins
>> servings: 4
>> source: https://example.com/recipe

@flour{200%g}

Mix.
`
	r, err := Parse(src, "")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if r.Times.Prep != "10 mins" || r.Times.Cook != "45 mins" || r.Times.Total != "" {
		t.Errorf("times = %+v", r.Times)
	}
	if r.Servings != "4" {
		t.Errorf("servings = %q", r.Servings)
	}
	if r.Provenance != "https://example.com/recipe" {
		t.Errorf("provenance = %q", r.Provenance)
	}
}

func TestOptionalAndSome(t *testing.T) {
	src := `>> title: X

@?thyme
@milk{}
@tomatoes{2-3}

Mix.
`
	r, err := Parse(src, "")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []Ingredient{
		{Name: "thyme", Optional: true},
		{Name: "milk", Amount: "some"},
		{Name: "tomatoes", Amount: "2-3"},
	}
	for i, w := range want {
		if r.Ingredients[i] != w {
			t.Errorf("ingredient[%d] = %+v, want %+v", i, r.Ingredients[i], w)
		}
	}
}

func TestUnclosedBlockComment(t *testing.T) {
	src := `>> title: X

@flour{200%g}

Start the dough.
[- forgot to close this comment
it continues here -] and resume
Knead.
`
	r, err := Parse(src, "")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// all four lines are one block (no blank lines), so one step
	if len(r.Steps) != 1 {
		t.Fatalf("got %d steps, want 1: %+v", len(r.Steps), r.Steps)
	}
	want := "forgot to close this comment it continues here"
	if len(r.Steps[0].Notes) != 1 || r.Steps[0].Notes[0] != want {
		t.Errorf("notes = %v, want [%q]", r.Steps[0].Notes, want)
	}
	for _, frag := range []string{"Start the dough.", "and resume", "Knead."} {
		if !strings.Contains(r.Steps[0].Text, frag) {
			t.Errorf("text missing %q: %q", frag, r.Steps[0].Text)
		}
	}
}

func TestFallbackTitle(t *testing.T) {
	src := "@flour{200%g}\n\nMix it up.\n"
	r, err := Parse(src, "Easy Flatbread")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if r.Title != "Easy Flatbread" {
		t.Errorf("title = %q, want fallback", r.Title)
	}
}

func TestTitleDerivedFromSteps(t *testing.T) {
	r, err := Parse("@flour{200%g}\n\nMix the flour with water and salt to make a simple dough.\n", "")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if r.Title != "Mix the flour with water and salt to ..." {
		t.Errorf("derived title = %q", r.Title)
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := Parse("   \n\n  ", "x"); err == nil {
		t.Error("empty source should error")
	}
	if _, err := Parse(">> source: nowhere\n", "x"); err != nil {
		t.Errorf("fallback title present, should not error: %v", err)
	}
}
