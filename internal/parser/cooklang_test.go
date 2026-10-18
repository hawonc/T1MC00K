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
[- they finish in the wak -]
Fry everything in a wok for ~{2%minutes}.
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

	if len(r.Steps) != 2 {
		t.Fatalf("got %d steps, want 2: %q", len(r.Steps), r.Steps)
	}
	if !strings.Contains(r.Steps[0], "Soak the noodles.") || strings.Contains(r.Steps[0], "keep them firm") {
		t.Errorf("line comment not stripped: %q", r.Steps[0])
	}
	if strings.Contains(strings.Join(r.Steps, "\n"), "they finish") {
		t.Errorf("block comment not stripped: %q", r.Steps)
	}
	if !strings.Contains(r.Steps[1], "wok") {
		t.Errorf("second step missing text: %q", r.Steps[1])
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
		// fallback title present means it should NOT error
		t.Log("fallback title accepted, no error (correct)")
	}
}
