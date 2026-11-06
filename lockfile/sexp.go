package lockfile

// An s-expression reader and writer.
//
// This is the whole of the syntax layer. It knows about parentheses, atoms,
// quoted strings and `;` comments, and nothing about schemas -- so a malformed
// file fails here, with a line number, before any code that means to read a
// field has to consider whether it is looking at one.

import (
	"fmt"
	"strings"
	"unicode"
)

// node is an atom or a list. A list has children; an atom has text.
type node struct {
	text     string
	children []node
	list     bool
	line     int
}

func atom(text string) node         { return node{text: text} }
func list(children ...node) node    { return node{children: children, list: true} }
func (n node) head() string         { return n.children[0].text }
func (n node) isList() bool         { return n.list }
func (n node) String() string       { return flatten(n) }
func (n node) at(i int) node        { return n.children[i] }
func (n node) size() int            { return len(n.children) }
func (n node) headIs(s string) bool { return n.list && n.size() > 0 && !n.at(0).list && n.head() == s }

// readForms parses a whole document into its top-level forms.
func readForms(text string) ([]node, error) {
	p := &parser{src: []rune(text), line: 1}
	var out []node
	for {
		p.skip()
		if p.done() {
			return out, nil
		}
		n, err := p.node()
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
}

type parser struct {
	src  []rune
	pos  int
	line int
}

func (p *parser) done() bool { return p.pos >= len(p.src) }

// skip advances past whitespace and comments.
func (p *parser) skip() {
	for !p.done() {
		switch c := p.src[p.pos]; {
		case c == '\n':
			p.line++
			p.pos++
		case unicode.IsSpace(c):
			p.pos++
		case c == ';':
			for !p.done() && p.src[p.pos] != '\n' {
				p.pos++
			}
		default:
			return
		}
	}
}

func (p *parser) node() (node, error) {
	line := p.line
	switch c := p.src[p.pos]; {
	case c == '(':
		p.pos++
		out := node{list: true, line: line}
		for {
			p.skip()
			if p.done() {
				return node{}, fmt.Errorf("line %d: a list is never closed", line)
			}
			if p.src[p.pos] == ')' {
				p.pos++
				return out, nil
			}
			child, err := p.node()
			if err != nil {
				return node{}, err
			}
			out.children = append(out.children, child)
		}
	case c == ')':
		return node{}, fmt.Errorf("line %d: a closing parenthesis with nothing open", p.line)
	case c == '"':
		return p.quoted()
	default:
		return p.bare(), nil
	}
}

func (p *parser) bare() node {
	start := p.pos
	for !p.done() && !isDelimiter(p.src[p.pos]) {
		p.pos++
	}
	return node{text: string(p.src[start:p.pos]), line: p.line}
}

func (p *parser) quoted() (node, error) {
	line := p.line
	p.pos++ // the opening quote
	var b strings.Builder
	for {
		if p.done() {
			return node{}, fmt.Errorf("line %d: a quoted string is never closed", line)
		}
		switch c := p.src[p.pos]; c {
		case '"':
			p.pos++
			return node{text: b.String(), line: line}, nil
		case '\\':
			p.pos++
			if p.done() {
				return node{}, fmt.Errorf("line %d: a quoted string ends in a backslash", line)
			}
			b.WriteRune(p.src[p.pos])
			p.pos++
		default:
			if c == '\n' {
				p.line++
			}
			b.WriteRune(c)
			p.pos++
		}
	}
}

func isDelimiter(c rune) bool {
	return unicode.IsSpace(c) || c == '(' || c == ')' || c == ';' || c == '"'
}

// printForms renders forms back to text.
//
// Leading atoms stay on the opening line and nested lists break onto their own,
// so a field is one line and a message is a block of them. That single rule is
// what makes the file read the way a hand-written one would.
func printForms(forms []node) string {
	var b strings.Builder
	for i, form := range forms {
		if i > 0 {
			b.WriteString("\n")
		}
		writeNode(&b, form, 0)
		b.WriteString("\n")
	}
	return b.String()
}

// writeNode prints a form.
//
// Only a top-level form breaks across lines. Everything nested stays on one
// line, however long it gets, so a message is a block of one-line fields and a
// rename is a one-line diff. A width limit would reflow the fields around the
// one that changed, which is the opposite of what a lock file is for.
func writeNode(b *strings.Builder, n node, depth int) {
	if !n.list || depth > 0 || !hasListChild(n) {
		b.WriteString(flatten(n))
		return
	}

	// Leading atoms stay on the opening line, so a message keeps its name
	// beside the keyword and only its contents break.
	lead := 0
	for lead < len(n.children) && !n.children[lead].list {
		lead++
	}

	b.WriteString("(")
	for i := 0; i < lead; i++ {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(quoteIfNeeded(n.children[i].text))
	}
	for _, child := range n.children[lead:] {
		b.WriteString("\n  ")
		writeNode(b, child, depth+1)
	}
	b.WriteString(")")
}

func hasListChild(n node) bool {
	for _, child := range n.children {
		if child.list {
			return true
		}
	}
	return false
}

// flatten renders a form on one line.
func flatten(n node) string {
	if !n.list {
		return quoteIfNeeded(n.text)
	}
	parts := make([]string, 0, len(n.children))
	for _, child := range n.children {
		parts = append(parts, flatten(child))
	}
	return "(" + strings.Join(parts, " ") + ")"
}

func quoteIfNeeded(text string) string {
	if text == "" {
		return `""`
	}
	for _, c := range text {
		if isDelimiter(c) {
			return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(text) + `"`
		}
	}
	return text
}
