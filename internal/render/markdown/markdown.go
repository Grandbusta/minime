// Package markdown reads a work's words as Markdown into a flat list of
// blocks for a format to lay out. Inline emphasis and links are kept as
// plain text, and every line break is kept, since most of what it writes is
// plain text.
package markdown

import (
	"fmt"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

type Kind int

const (
	Paragraph Kind = iota
	Heading
	Item // a list item's first lines; what else it holds follows, one Depth in
	Code
	Rule
	Table
)

type Block struct {
	Kind  Kind
	Text  string // line breaks kept; "" for a rule or a table
	Level int    // a heading's, from 1
	Depth int    // how far in: list items and quotes go one deeper
	Quote bool   // inside a quote
	Mark  string // an item's "•" or "3."
	Tight bool   // a paragraph in a tight list: no space after it
	Rows  [][]string
}

var parser = goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough)).Parser()

// Blocks reads body, leaving out a first line that repeats title.
func Blocks(title, body string) []Block {
	body = strings.TrimSpace(body)
	if first, rest, _ := strings.Cut(body, "\n"); title != "" && strings.EqualFold(strings.Trim(first, " *#\"'"), strings.TrimSpace(title)) {
		body = strings.TrimSpace(rest)
	}
	r := reader{src: []byte(body)}
	root := parser.Parse(text.NewReader(r.src))
	for n := root.FirstChild(); n != nil; n = n.NextSibling() {
		r.block(n, 0, false)
	}
	return r.out
}

type reader struct {
	src []byte
	out []Block
}

func (r *reader) block(n ast.Node, depth int, quote bool) {
	switch n := n.(type) {
	case *ast.Heading:
		r.out = append(r.out, Block{Kind: Heading, Text: r.inline(n), Level: n.Level, Depth: depth, Quote: quote})
	case *ast.Paragraph, *ast.TextBlock:
		_, tight := n.(*ast.TextBlock)
		r.out = append(r.out, Block{Kind: Paragraph, Text: r.inline(n), Depth: depth, Quote: quote, Tight: tight})
	case *ast.List:
		num := n.Start
		for item := n.FirstChild(); item != nil; item = item.NextSibling() {
			mark := "•"
			if n.IsOrdered() {
				mark = fmt.Sprintf("%d.", num)
				num++
			}
			b := Block{Kind: Item, Mark: mark, Depth: depth, Quote: quote}
			c := item.FirstChild()
			if c != nil && (c.Kind() == ast.KindParagraph || c.Kind() == ast.KindTextBlock) {
				b.Text, c = r.inline(c), c.NextSibling()
			}
			r.out = append(r.out, b)
			for ; c != nil; c = c.NextSibling() {
				r.block(c, depth+1, quote)
			}
		}
	case *ast.Blockquote:
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			r.block(c, depth+1, true)
		}
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		var b strings.Builder
		ls := n.Lines()
		for i := 0; i < ls.Len(); i++ {
			seg := ls.At(i)
			b.Write(seg.Value(r.src))
		}
		r.out = append(r.out, Block{Kind: Code, Text: strings.TrimRight(b.String(), "\n"), Depth: depth, Quote: quote})
	case *ast.ThematicBreak:
		r.out = append(r.out, Block{Kind: Rule, Depth: depth})
	case *east.Table:
		var rows [][]string
		for row := n.FirstChild(); row != nil; row = row.NextSibling() {
			var cells []string
			for c := row.FirstChild(); c != nil; c = c.NextSibling() {
				cells = append(cells, r.inline(c))
			}
			rows = append(rows, cells)
		}
		r.out = append(r.out, Block{Kind: Table, Rows: rows, Depth: depth, Quote: quote})
	case *ast.HTMLBlock:
		// left out
	default:
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			r.block(c, depth, quote)
		}
	}
}

func (r *reader) inline(n ast.Node) string {
	var b strings.Builder
	var walk func(ast.Node)
	walk = func(n ast.Node) {
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch c := c.(type) {
			case *ast.Text:
				b.Write(c.Segment.Value(r.src))
				if c.HardLineBreak() || c.SoftLineBreak() { // a line break in plain writing is meant
					b.WriteString("\n")
				}
			case *ast.String:
				b.Write(c.Value)
			case *ast.AutoLink:
				b.Write(c.URL(r.src))
			case *ast.RawHTML:
			default:
				walk(c)
			}
		}
	}
	walk(n)
	return b.String()
}
