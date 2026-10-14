package frontmatter

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParse(t *testing.T) {
	data := "---\r\n" +
		"name: pdf\r\n" +
		"description: \"Fill PDF forms: text and checkboxes.\"\r\n" +
		"# a comment\r\n" +
		"notes: |\r\n" +
		"  first\r\n" +
		"  second\r\n" +
		"summary: >\r\n" +
		"  one\r\n" +
		"  line\r\n" +
		"---\r\n" +
		"\r\n# PDF\r\nSteps.\r\n"
	got, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := Document{
		Fields: map[string]string{
			"name":        "pdf",
			"description": "Fill PDF forms: text and checkboxes.",
			"notes":       "first\nsecond",
			"summary":     "one line",
		},
		Body: "# PDF\nSteps.",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Parse mismatch (-want +got):\n%s", diff)
	}
}

func TestParseEmptyBody(t *testing.T) {
	got, err := Parse([]byte("---\nname: x\n---"))
	if err != nil || got.Fields["name"] != "x" || got.Body != "" {
		t.Errorf("Parse = %+v, %v", got, err)
	}
}

func TestParseErrors(t *testing.T) {
	for _, data := range []string{"no header", "---\nname: x\n"} {
		if _, err := Parse([]byte(data)); err == nil {
			t.Errorf("Parse(%q) succeeded", data)
		}
	}
}

func TestList(t *testing.T) {
	if diff := cmp.Diff([]string{"Read", "Glob"}, List(`[Read, "Glob", ]`)); diff != "" {
		t.Errorf("List mismatch (-want +got):\n%s", diff)
	}
	if List("") != nil {
		t.Error("List of an empty value is not empty")
	}
}
