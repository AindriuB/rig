package jira

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got with testdata/text/<name>, or rewrites it with -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "text", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s:\n got: %q\nwant: %q", name, got, want)
	}
}

func readText(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "text", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// AC5: ADF -> Markdown.
func TestADFToMarkdown(t *testing.T) {
	got, err := adfToMarkdown(readText(t, "description.adf.json"))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "description.adf.md", got+"\n")
}

func TestADFToMarkdownEdges(t *testing.T) {
	for in, want := range map[string]string{
		`null`: "", ``: "", `"plain"`: "plain",
		`{"type":"doc","content":[{"type":"mystery","content":[{"type":"text","text":"kept"}]}]}`:                                                         "kept",
		`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":" b ","marks":[{"type":"strong"}]},{"type":"text","text":"x"}]}]}`: " **b** x",
	} {
		got, err := adfToMarkdown(json.RawMessage(in))
		if err != nil || got != strings.TrimSpace(want) && got != want {
			t.Errorf("adfToMarkdown(%s) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := adfToMarkdown(json.RawMessage(`[1]`)); err == nil {
		t.Error("a JSON array is not a document")
	}
}

// AC5: wiki markup -> Markdown.
func TestWikiToMarkdown(t *testing.T) {
	golden(t, "description.wiki.md", wikiToMarkdown(string(readText(t, "description.wiki.txt")))+"\n")
}

// AC5: Markdown -> ADF and Markdown -> wiki.
func TestMarkdownToADF(t *testing.T) {
	doc := markdownToADF(string(readText(t, "comment.md")))
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "comment.adf.json", string(b)+"\n")
}

func TestMarkdownToWiki(t *testing.T) {
	golden(t, "comment.wiki.txt", markdownToWiki(string(readText(t, "comment.md")))+"\n")
}

func TestMarkdownToADFPlainAndEmpty(t *testing.T) {
	for _, in := range []string{"", "   ", "just text"} {
		doc := markdownToADF(in)
		if doc.Type != "doc" || len(doc.Content) != 1 || doc.Content[0].Type != "paragraph" {
			t.Errorf("markdownToADF(%q) = %+v", in, doc)
		}
	}
}

// What the adapter writes it can read back.
func TestMarkdownRoundTrips(t *testing.T) {
	const md = "# Title\n\nSome **bold**, *italic* and `code` with a [link](https://example.com/x).\n\n- one\n- two\n  - nested\n\n1. first\n2. second\n\n```go\nfmt.Println(1)\n```"
	b, _ := json.Marshal(markdownToADF(md))
	if got, err := adfToMarkdown(b); err != nil || got != md {
		t.Errorf("ADF round trip:\n got: %q, %v\nwant: %q", got, err, md)
	}
	if got := wikiToMarkdown(markdownToWiki(md)); got != md {
		t.Errorf("wiki round trip:\n got: %q\nwant: %q", got, md)
	}
}

func TestWikiInlineLeavesSnakeCaseAndStars(t *testing.T) {
	for in, want := range map[string]string{
		"use snake_case_name here": "use snake_case_name here",
		"2 * 3 * 4":                "2 * 3 * 4",
		"*b* and _i_":              "**b** and *i*",
		"{color:red}x{color}":      "x",
		"[a|https://e.io/p]":       "[a](https://e.io/p)",
		"[~jdoe] hi":               "@jdoe hi",
	} {
		if got := wikiInline(in); got != want {
			t.Errorf("wikiInline(%q) = %q, want %q", in, got, want)
		}
	}
}

// 37-1: a list that switches kind keeps every item, one top-level list per run.
func TestMarkdownToADFMixedListKinds(t *testing.T) {
	b, _ := json.Marshal(markdownToADF("- a\n1. b"))
	const want = `{"version":1,"type":"doc","content":[{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]}]}]},{"type":"orderedList","attrs":{"order":1},"content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"b"}]}]}]}]}`
	if string(b) != want {
		t.Errorf("got:  %s\nwant: %s", b, want)
	}
}

// 37-2: a quote keeps the lists inside it; R2: only schema-allowed children.
func TestMarkdownToADFQuoteKeepsBlocks(t *testing.T) {
	doc := markdownToADF("> # Head\n> - item one")
	b, _ := json.Marshal(doc)
	s := string(b)
	for _, w := range []string{`"type":"blockquote"`, `"type":"bulletList"`, `"text":"Head"`, `"text":"item one"`} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %s in %s", w, s)
		}
	}
	checkQuoteChildren(t, doc)
}

func checkQuoteChildren(t *testing.T, n adfNode) {
	t.Helper()
	if n.Type == "blockquote" {
		for _, c := range n.Content {
			switch c.Type {
			case "paragraph", "bulletList", "orderedList", "codeBlock":
			default:
				t.Errorf("blockquote child %q not allowed", c.Type)
			}
		}
	}
	for _, c := range n.Content {
		checkQuoteChildren(t, c)
	}
}

func TestMarkdownToADFNestedQuoteFlat(t *testing.T) {
	doc := markdownToADF("> > a")
	if len(doc.Content) != 1 || doc.Content[0].Type != "blockquote" || len(doc.Content[0].Content) != 1 || doc.Content[0].Content[0].Type != "paragraph" {
		b, _ := json.Marshal(doc)
		t.Fatalf("got %s", b)
	}
	checkQuoteChildren(t, doc)
}

func TestMarkdownToADFDeepQuote(t *testing.T) {
	doc := markdownToADF(strings.Repeat(">", 10000) + " x")
	b, err := json.Marshal(doc)
	if err != nil || !strings.Contains(string(b), `"text":"x"`) {
		t.Fatalf("err=%v", err)
	}
	checkQuoteChildren(t, doc)
}

func TestMarkdownToADFQuotedHeadingIsParagraph(t *testing.T) {
	doc := markdownToADF("> # Head")
	b, _ := json.Marshal(doc)
	if strings.Contains(string(b), `"heading"`) || !strings.Contains(string(b), `"text":"Head"`) {
		t.Errorf("got %s", b)
	}
	checkQuoteChildren(t, doc)
}
