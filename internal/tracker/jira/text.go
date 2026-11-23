package jira

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The text conversions cover one subset: paragraphs, headings, bullet and
// ordered lists (nested), fenced code blocks, block quotes, and the inline
// marks bold, italic, code and links. Anything else becomes its plain text.

// ---------------------------------------------------------------------------
// Atlassian Document Format -> Markdown

type adfNode struct {
	Version int       `json:"version,omitempty"` // 1, on the doc node only
	Type    string    `json:"type"`
	Text    string    `json:"text,omitempty"`
	Attrs   *adfAttrs `json:"attrs,omitempty"`
	Marks   []adfMark `json:"marks,omitempty"`
	Content []adfNode `json:"content,omitempty"`
}

type adfAttrs struct {
	Level    int    `json:"level,omitempty"`
	Language string `json:"language,omitempty"`
	Order    int    `json:"order,omitempty"`
	URL      string `json:"url,omitempty"`
	// ShortName and Text name an emoji or a mention.
	ShortName string `json:"shortName,omitempty"`
	Display   string `json:"text,omitempty"`
}

type adfMark struct {
	Type  string `json:"type"`
	Attrs *struct {
		Href string `json:"href,omitempty"`
	} `json:"attrs,omitempty"`
}

// adfToMarkdown converts an ADF document (the JSON of a Cloud description or
// comment) to Markdown. A value that is not a document is treated as an empty
// description; a JSON string is returned as it is.
func adfToMarkdown(raw json.RawMessage) (string, error) {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", fmt.Errorf("jira: decode text: %w", err)
		}
		return s, nil
	}
	var doc adfNode
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("jira: decode document: %w", err)
	}
	return strings.TrimSpace(adfBlocks(doc.Content, 0)), nil
}

// adfBlocks renders sibling block nodes separated by a blank line.
func adfBlocks(nodes []adfNode, depth int) string {
	var parts []string
	for _, n := range nodes {
		if s := adfBlock(n, depth); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}

func adfBlock(n adfNode, depth int) string {
	switch n.Type {
	case "paragraph":
		return adfInlines(n.Content)
	case "heading":
		level := 1
		if n.Attrs != nil && n.Attrs.Level >= 1 && n.Attrs.Level <= 6 {
			level = n.Attrs.Level
		}
		return strings.Repeat("#", level) + " " + adfInlines(n.Content)
	case "bulletList", "orderedList":
		return adfList(n, depth)
	case "codeBlock":
		lang := ""
		if n.Attrs != nil {
			lang = n.Attrs.Language
		}
		return "```" + lang + "\n" + adfPlain(n.Content) + "\n```"
	case "blockquote":
		inner := adfBlocks(n.Content, depth)
		lines := strings.Split(inner, "\n")
		for i, l := range lines {
			lines[i] = strings.TrimRight("> "+l, " ")
		}
		return strings.Join(lines, "\n")
	case "rule":
		return "---"
	case "text", "hardBreak", "mention", "emoji", "inlineCard":
		return adfInlines([]adfNode{n})
	}
	// An unknown block (a panel, a table, a media group): its text.
	var parts []string
	for _, c := range n.Content {
		if s := adfBlock(c, depth); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

func adfList(n adfNode, depth int) string {
	start := 1
	if n.Type == "orderedList" && n.Attrs != nil && n.Attrs.Order > 0 {
		start = n.Attrs.Order
	}
	var lines []string
	for i, item := range n.Content {
		marker := "-"
		if n.Type == "orderedList" {
			marker = fmt.Sprintf("%d.", start+i)
		}
		indent := strings.Repeat("  ", depth)
		var head []string
		var nested []string
		for _, c := range item.Content {
			switch c.Type {
			case "bulletList", "orderedList":
				nested = append(nested, adfList(c, depth+1))
			default:
				if s := adfBlock(c, depth+1); s != "" {
					head = append(head, s)
				}
			}
		}
		text := strings.Join(head, " ")
		lines = append(lines, strings.TrimRight(indent+marker+" "+strings.ReplaceAll(text, "\n", " "), " "))
		lines = append(lines, nested...)
	}
	return strings.Join(lines, "\n")
}

func adfPlain(nodes []adfNode) string {
	var b strings.Builder
	for _, n := range nodes {
		switch n.Type {
		case "hardBreak":
			b.WriteString("\n")
		default:
			b.WriteString(n.Text)
			b.WriteString(adfPlain(n.Content))
		}
	}
	return b.String()
}

func adfInlines(nodes []adfNode) string {
	var b strings.Builder
	for _, n := range nodes {
		switch n.Type {
		case "text":
			b.WriteString(adfMarked(n))
		case "hardBreak":
			b.WriteString("\n")
		case "mention":
			if n.Attrs != nil && n.Attrs.Display != "" {
				b.WriteString(n.Attrs.Display)
			}
		case "emoji":
			if n.Attrs != nil {
				if n.Attrs.Display != "" {
					b.WriteString(n.Attrs.Display)
				} else {
					b.WriteString(n.Attrs.ShortName)
				}
			}
		case "inlineCard":
			if n.Attrs != nil {
				b.WriteString(n.Attrs.URL)
			}
		default:
			b.WriteString(n.Text)
			b.WriteString(adfInlines(n.Content))
		}
	}
	return b.String()
}

// adfMarked wraps a text node in the Markdown of its marks. Spaces at the
// edges stay outside the markers, which CommonMark requires.
func adfMarked(n adfNode) string {
	core := strings.TrimSpace(n.Text)
	if core == "" {
		return n.Text
	}
	lead := n.Text[:len(n.Text)-len(strings.TrimLeftFunc(n.Text, unicode.IsSpace))]
	trail := n.Text[len(strings.TrimRightFunc(n.Text, unicode.IsSpace)):]
	href := ""
	for _, m := range n.Marks {
		switch m.Type {
		case "code":
			core = "`" + core + "`"
		case "strong":
			core = "**" + core + "**"
		case "em":
			core = "*" + core + "*"
		case "strike":
			core = "~~" + core + "~~"
		case "link":
			if m.Attrs != nil {
				href = m.Attrs.Href
			}
		}
	}
	if href != "" {
		core = "[" + core + "](" + href + ")"
	}
	return lead + core + trail
}

// ---------------------------------------------------------------------------
// Markdown -> blocks and inline spans (shared by the ADF and wiki writers)

type blockKind int

const (
	blockPara blockKind = iota
	blockHeading
	blockCode
	blockList
	blockQuote
)

type listItem struct {
	depth   int
	ordered bool
	text    string
}

type mdBlock struct {
	kind  blockKind
	level int      // heading level
	lang  string   // code language
	lines []string // paragraph, code or quote lines
	items []listItem
}

var (
	headingRE  = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	listItemRE = regexp.MustCompile(`^([ \t]*)([-*+]|\d+[.)])\s+(.*)$`)
	fenceRE    = regexp.MustCompile("^\\s*```\\s*([A-Za-z0-9_+.#-]*)\\s*$")
)

// parseMarkdown splits Markdown into blocks.
func parseMarkdown(src string) []mdBlock {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(src, "\r\n", "\n"), "\r", "\n"), "\n")
	var blocks []mdBlock
	var para []string
	flush := func() {
		if len(para) > 0 {
			blocks = append(blocks, mdBlock{kind: blockPara, lines: para})
			para = nil
		}
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if m := fenceRE.FindStringSubmatch(line); m != nil {
			flush()
			var code []string
			for i++; i < len(lines) && !fenceRE.MatchString(lines[i]); i++ {
				code = append(code, lines[i])
			}
			blocks = append(blocks, mdBlock{kind: blockCode, lang: m[1], lines: code})
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if m := headingRE.FindStringSubmatch(line); m != nil {
			flush()
			blocks = append(blocks, mdBlock{kind: blockHeading, level: len(m[1]), lines: []string{m[2]}})
			continue
		}
		if m := listItemRE.FindStringSubmatch(line); m != nil {
			flush()
			if n := len(blocks); n > 0 && blocks[n-1].kind == blockList && lastBlockEndedAt(blocks, i, lines) {
				blocks[n-1].items = append(blocks[n-1].items, newListItem(m, &blocks[n-1]))
			} else {
				b := mdBlock{kind: blockList}
				b.items = append(b.items, newListItem(m, &b))
				blocks = append(blocks, b)
			}
			continue
		}
		if strings.HasPrefix(line, ">") {
			flush()
			text := strings.TrimPrefix(strings.TrimPrefix(line, ">"), " ")
			if n := len(blocks); n > 0 && blocks[n-1].kind == blockQuote && i > 0 && strings.HasPrefix(lines[i-1], ">") {
				blocks[n-1].lines = append(blocks[n-1].lines, text)
			} else {
				blocks = append(blocks, mdBlock{kind: blockQuote, lines: []string{text}})
			}
			continue
		}
		para = append(para, strings.TrimRight(line, " \t"))
	}
	flush()
	return blocks
}

// lastBlockEndedAt reports whether the previous line was a list item, so a
// list item after a blank line starts a new list.
func lastBlockEndedAt(_ []mdBlock, i int, lines []string) bool {
	return i > 0 && listItemRE.MatchString(lines[i-1])
}

// newListItem derives the nesting depth of a list line from its indent,
// relative to the items already in b.
func newListItem(m []string, b *mdBlock) listItem {
	indent := 0
	for _, r := range m[1] {
		if r == '\t' {
			indent += 2
		} else {
			indent++
		}
	}
	depth := 0
	if n := len(b.items); n > 0 {
		prev := b.items[n-1]
		prevIndent := prev.depth * 2
		switch {
		case indent > prevIndent:
			depth = prev.depth + 1
		default:
			depth = prev.depth
			for depth > 0 && indent < depth*2 {
				depth--
			}
		}
	}
	return listItem{depth: depth, ordered: m[2][0] >= '0' && m[2][0] <= '9', text: m[3]}
}

// span is a run of inline text with its marks.
type span struct {
	text   string
	bold   bool
	italic bool
	strike bool
	code   bool
	href   string
}

// parseInline splits one line (or several joined by "\n") of Markdown into
// spans. An unmatched marker is literal text.
func parseInline(s string) []span { return inlineSpans(s, span{}) }

func inlineSpans(s string, base span) []span {
	var out []span
	var lit strings.Builder
	emit := func(sp span) {
		if sp.text != "" {
			out = append(out, sp)
		}
	}
	flush := func() {
		sp := base
		sp.text = lit.String()
		lit.Reset()
		emit(sp)
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && strings.IndexByte("\\`*_[]()#+-.!~{}<>|", s[i+1]) >= 0:
			lit.WriteByte(s[i+1])
			i += 2
			continue
		case c == '`':
			if j := strings.IndexByte(s[i+1:], '`'); j > 0 {
				flush()
				sp := base
				sp.text, sp.code = s[i+1:i+1+j], true
				emit(sp)
				i += j + 2
				continue
			}
		case c == '[':
			if txt, href, n, ok := mdLink(s[i:]); ok {
				flush()
				inner := base
				inner.href = href
				out = append(out, inlineSpans(txt, inner)...)
				i += n
				continue
			}
		case strings.HasPrefix(s[i:], "**"), strings.HasPrefix(s[i:], "~~"):
			mark := s[i : i+2]
			if j := strings.Index(s[i+2:], mark); j > 0 {
				flush()
				inner := base
				if mark == "**" {
					inner.bold = true
				} else {
					inner.strike = true
				}
				out = append(out, inlineSpans(s[i+2:i+2+j], inner)...)
				i += j + 4
				continue
			}
		case c == '*' || c == '_':
			if closeAt := emphasisEnd(s, i); closeAt > 0 {
				flush()
				inner := base
				inner.italic = true
				out = append(out, inlineSpans(s[i+1:closeAt], inner)...)
				i = closeAt + 1
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		lit.WriteString(s[i : i+size])
		i += size
	}
	flush()
	return out
}

// emphasisEnd returns the index of the marker closing the one at s[i], or -1.
// An opening marker needs a non-space after it and, for '_', a non-word before
// it, so snake_case stays literal.
func emphasisEnd(s string, i int) int {
	c := s[i]
	if i+1 >= len(s) || s[i+1] == ' ' || s[i+1] == c {
		return -1
	}
	if c == '_' && i > 0 {
		if r, _ := utf8.DecodeLastRuneInString(s[:i]); unicode.IsLetter(r) || unicode.IsDigit(r) {
			return -1
		}
	}
	for j := i + 2; j < len(s); j++ {
		if s[j] != c || s[j-1] == ' ' || s[j-1] == '\\' {
			continue
		}
		if j+1 < len(s) && s[j+1] == c {
			continue
		}
		if c == '_' && j+1 < len(s) {
			if r, _ := utf8.DecodeRuneInString(s[j+1:]); unicode.IsLetter(r) || unicode.IsDigit(r) {
				continue
			}
		}
		return j
	}
	return -1
}

// mdLink parses "[text](href)" at the start of s and returns the number of
// bytes it spans.
func mdLink(s string) (text, href string, n int, ok bool) {
	depth := 0
	end := -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 || end+1 >= len(s) || s[end+1] != '(' {
		return "", "", 0, false
	}
	close := strings.IndexByte(s[end+2:], ')')
	if close < 0 {
		return "", "", 0, false
	}
	href = strings.TrimSpace(s[end+2 : end+2+close])
	if href == "" || strings.ContainsAny(href, " \t\n") {
		return "", "", 0, false
	}
	return s[1:end], href, end + 3 + close, true
}

// ---------------------------------------------------------------------------
// Markdown -> ADF

// markdownToADF converts Markdown to an ADF document value for a v3 comment
// body. The document always has at least one paragraph.
func markdownToADF(md string) adfNode {
	doc := adfNode{Version: 1, Type: "doc", Content: []adfNode{}}
	for _, b := range parseMarkdown(md) {
		switch b.kind {
		case blockPara:
			doc.Content = append(doc.Content, adfNode{Type: "paragraph", Content: adfSpans(strings.Join(b.lines, "\n"))})
		case blockHeading:
			doc.Content = append(doc.Content, adfNode{Type: "heading", Attrs: &adfAttrs{Level: b.level}, Content: adfSpans(b.lines[0])})
		case blockCode:
			n := adfNode{Type: "codeBlock"}
			if b.lang != "" {
				n.Attrs = &adfAttrs{Language: b.lang}
			}
			if code := strings.Join(b.lines, "\n"); code != "" {
				n.Content = []adfNode{{Type: "text", Text: code}}
			}
			doc.Content = append(doc.Content, n)
		case blockQuote:
			var inner []adfNode
			for _, p := range parseMarkdown(strings.Join(b.lines, "\n")) {
				inner = append(inner, adfNode{Type: "paragraph", Content: adfSpans(strings.Join(p.lines, "\n"))})
			}
			if len(inner) == 0 {
				inner = []adfNode{{Type: "paragraph", Content: []adfNode{}}}
			}
			doc.Content = append(doc.Content, adfNode{Type: "blockquote", Content: inner})
		case blockList:
			doc.Content = append(doc.Content, adfListFrom(b.items)...)
		}
	}
	if len(doc.Content) == 0 {
		doc.Content = []adfNode{{Type: "paragraph", Content: []adfNode{}}}
	}
	return doc
}

// adfListFrom builds nested lists from flat items; each run of one list kind
// becomes its own top-level list.
func adfListFrom(items []listItem) []adfNode {
	var lists []adfNode
	for i := 0; i < len(items); {
		var list adfNode
		list, i = adfListAt(items, i, items[i].depth)
		lists = append(lists, list)
	}
	return lists
}

func adfListAt(items []listItem, i, depth int) (adfNode, int) {
	list := adfNode{Type: "bulletList"}
	if items[i].ordered {
		list.Type = "orderedList"
		list.Attrs = &adfAttrs{Order: 1}
	}
	for i < len(items) && items[i].depth >= depth {
		if items[i].depth > depth {
			// A deeper item with no parent item at this depth: attach to the last.
			var sub adfNode
			sub, i = adfListAt(items, i, items[i].depth)
			if n := len(list.Content); n > 0 {
				list.Content[n-1].Content = append(list.Content[n-1].Content, sub)
			} else {
				list.Content = append(list.Content, adfNode{Type: "listItem", Content: []adfNode{sub}})
			}
			continue
		}
		if items[i].ordered != (list.Type == "orderedList") {
			break
		}
		it := adfNode{Type: "listItem", Content: []adfNode{{Type: "paragraph", Content: adfSpans(items[i].text)}}}
		list.Content = append(list.Content, it)
		i++
	}
	return list, i
}

func adfSpans(s string) []adfNode {
	out := []adfNode{}
	for _, sp := range parseInline(s) {
		// A newline inside a span becomes a hard break.
		for k, part := range strings.Split(sp.text, "\n") {
			if k > 0 {
				out = append(out, adfNode{Type: "hardBreak"})
			}
			if part == "" {
				continue
			}
			n := adfNode{Type: "text", Text: part}
			if sp.code {
				n.Marks = append(n.Marks, adfMark{Type: "code"})
			}
			if sp.bold {
				n.Marks = append(n.Marks, adfMark{Type: "strong"})
			}
			if sp.italic {
				n.Marks = append(n.Marks, adfMark{Type: "em"})
			}
			if sp.strike {
				n.Marks = append(n.Marks, adfMark{Type: "strike"})
			}
			if sp.href != "" {
				m := adfMark{Type: "link"}
				m.Attrs = &struct {
					Href string `json:"href,omitempty"`
				}{Href: sp.href}
				n.Marks = append(n.Marks, m)
			}
			out = append(out, n)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Wiki markup -> Markdown

var (
	wikiHeadingRE = regexp.MustCompile(`^h([1-6])\.\s+(.*)$`)
	wikiListRE    = regexp.MustCompile(`^([*#-]+)\s+(.*)$`)
	wikiCodeRE    = regexp.MustCompile(`^\{(code|noformat)(?::([^}]*))?\}\s*(.*)$`)
	wikiRuleRE    = regexp.MustCompile(`^-{4,}$`)
	wikiMacroRE   = regexp.MustCompile(`^\{(color|anchor|panel|quote)(:[^}]*)?\}`)
)

// wikiToMarkdown converts Jira wiki markup (a Server/Data Center description
// or comment) to Markdown.
func wikiToMarkdown(src string) string {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(src, "\r\n", "\n"), "\r", "\n"), "\n")
	var out []string
	num := map[int]int{} // ordered-list counter per depth
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if t := strings.TrimSpace(line); !wikiListRE.MatchString(t) || strings.HasPrefix(t, "--") {
			clear(num)
		}
		if m := wikiCodeRE.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			tag := m[1]
			lang := ""
			if tag == "code" {
				lang = strings.TrimSpace(strings.SplitN(m[2], "|", 2)[0])
				if strings.Contains(lang, "=") { // {code:title=x}
					lang = ""
				}
			}
			var code []string
			rest := m[3]
			closeTag := "{" + tag + "}"
			if k := strings.Index(rest, closeTag); k >= 0 { // one-line block
				code = append(code, rest[:k])
			} else {
				if rest != "" {
					code = append(code, rest)
				}
				for i++; i < len(lines); i++ {
					if k := strings.Index(lines[i], closeTag); k >= 0 {
						if k > 0 {
							code = append(code, lines[i][:k])
						}
						break
					}
					code = append(code, lines[i])
				}
			}
			out = append(out, "```"+lang)
			out = append(out, code...)
			out = append(out, "```")
			continue
		}
		trim := strings.TrimSpace(line)
		switch {
		case wikiHeadingRE.MatchString(trim):
			m := wikiHeadingRE.FindStringSubmatch(trim)
			out = append(out, strings.Repeat("#", int(m[1][0]-'0'))+" "+wikiInline(m[2]))
		case strings.HasPrefix(trim, "bq. "):
			out = append(out, "> "+wikiInline(strings.TrimPrefix(trim, "bq. ")))
		case trim == "{quote}":
			var q []string
			for i++; i < len(lines) && strings.TrimSpace(lines[i]) != "{quote}"; i++ {
				q = append(q, "> "+wikiInline(lines[i]))
			}
			out = append(out, q...)
		case wikiRuleRE.MatchString(trim):
			out = append(out, "---")
		case wikiListRE.MatchString(trim) && !strings.HasPrefix(trim, "--"):
			m := wikiListRE.FindStringSubmatch(trim)
			marker := m[1]
			depth := len(marker) - 1
			bullet := "-"
			for d := range num {
				if d > depth {
					delete(num, d)
				}
			}
			if marker[len(marker)-1] == '#' {
				num[depth]++
				bullet = fmt.Sprintf("%d.", num[depth])
			} else {
				delete(num, depth)
			}
			out = append(out, strings.Repeat("  ", depth)+bullet+" "+wikiInline(m[2]))
		default:
			out = append(out, wikiInline(strings.TrimRight(line, " \t")))
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// wikiInline converts the inline marks of one wiki line to Markdown.
func wikiInline(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		rest := s[i:]
		switch {
		case strings.HasPrefix(rest, "{{"):
			if j := strings.Index(rest[2:], "}}"); j >= 0 {
				b.WriteString("`" + rest[2:2+j] + "`")
				i += j + 4
				continue
			}
		case strings.HasPrefix(rest, `\\`):
			b.WriteString("\n")
			i += 2
			continue
		case rest[0] == '{':
			if m := wikiMacroRE.FindString(rest); m != "" {
				i += len(m)
				continue
			}
		case rest[0] == '[':
			if j := strings.IndexByte(rest, ']'); j > 1 {
				inner := rest[1:j]
				switch {
				case strings.HasPrefix(inner, "~"):
					b.WriteString("@" + inner[1:])
					i += j + 1
					continue
				case strings.Contains(inner, "|"):
					text, href, _ := strings.Cut(inner, "|")
					href, _, _ = strings.Cut(href, "|")
					if strings.Contains(href, "://") || strings.HasPrefix(href, "mailto:") {
						b.WriteString("[" + text + "](" + href + ")")
						i += j + 1
						continue
					}
				case strings.Contains(inner, "://"):
					b.WriteString("[" + inner + "](" + inner + ")")
					i += j + 1
					continue
				}
			}
		case rest[0] == '*' || rest[0] == '_':
			if end := wikiEmphasisEnd(s, i); end > 0 {
				mark := "**"
				if rest[0] == '_' {
					mark = "*"
				}
				b.WriteString(mark + wikiInline(s[i+1:end]) + mark)
				i = end + 1
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(rest)
		b.WriteString(rest[:size])
		i += size
	}
	return b.String()
}

// wikiEmphasisEnd returns the index of the marker closing the one at s[i], or
// -1. The opening marker follows a non-word character and precedes a non-space;
// the closing one follows a non-space and precedes a non-word character.
func wikiEmphasisEnd(s string, i int) int {
	c := s[i]
	if i+1 >= len(s) || s[i+1] == ' ' {
		return -1
	}
	if i > 0 {
		if r, _ := utf8.DecodeLastRuneInString(s[:i]); unicode.IsLetter(r) || unicode.IsDigit(r) || r == rune(c) {
			return -1
		}
	}
	for j := i + 2; j < len(s); j++ {
		if s[j] != c || s[j-1] == ' ' {
			continue
		}
		if j+1 < len(s) {
			if r, _ := utf8.DecodeRuneInString(s[j+1:]); unicode.IsLetter(r) || unicode.IsDigit(r) {
				continue
			}
		}
		return j
	}
	return -1
}

// ---------------------------------------------------------------------------
// Markdown -> wiki markup

// markdownToWiki converts Markdown to Jira wiki markup for a v2 comment body.
func markdownToWiki(md string) string {
	var out []string
	for _, b := range parseMarkdown(md) {
		switch b.kind {
		case blockPara:
			out = append(out, wikiSpans(strings.Join(b.lines, "\n")))
		case blockHeading:
			out = append(out, fmt.Sprintf("h%d. %s", b.level, wikiSpans(b.lines[0])))
		case blockCode:
			head := "{code}"
			if b.lang != "" {
				head = "{code:" + b.lang + "}"
			}
			out = append(out, head+"\n"+strings.Join(b.lines, "\n")+"\n{code}")
		case blockQuote:
			out = append(out, "{quote}\n"+wikiSpans(strings.Join(b.lines, "\n"))+"\n{quote}")
		case blockList:
			var lines []string
			var kinds []byte
			for _, it := range b.items {
				d := it.depth
				if d > len(kinds) {
					d = len(kinds)
				}
				kinds = kinds[:d]
				if it.ordered {
					kinds = append(kinds, '#')
				} else {
					kinds = append(kinds, '*')
				}
				lines = append(lines, string(kinds)+" "+wikiSpans(it.text))
			}
			out = append(out, strings.Join(lines, "\n"))
		}
	}
	return strings.Join(out, "\n\n")
}

var wikiEscaper = strings.NewReplacer(`{`, `\{`, `[`, `\[`, `*`, `\*`, `_`, `\_`)

func wikiSpans(s string) string {
	var b strings.Builder
	for _, sp := range parseInline(s) {
		var t string
		switch {
		case sp.code:
			t = "{{" + sp.text + "}}"
		default:
			t = wikiEscaper.Replace(sp.text)
			if core := strings.TrimSpace(t); core != "" {
				lead := t[:strings.Index(t, core)]
				trail := t[len(lead)+len(core):]
				if sp.bold {
					core = "*" + core + "*"
				}
				if sp.italic {
					core = "_" + core + "_"
				}
				if sp.strike {
					core = "-" + core + "-"
				}
				t = lead + core + trail
			}
		}
		if sp.href != "" {
			t = "[" + strings.TrimSpace(t) + "|" + sp.href + "]"
		}
		b.WriteString(t)
	}
	return b.String()
}
