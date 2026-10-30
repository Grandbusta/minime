// Package pdf lays a piece of writing out as an A4 PDF: its title, then its
// text read as Markdown (headings, paragraphs, lists, quotes, code, tables
// and rules). Inline emphasis and links are kept as plain text, and every
// line break is kept, since most of what it writes is plain text.
package pdf

import (
	"bytes"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/signintech/gopdf"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"
)

const (
	pageW, pageH = 595.0, 842.0
	margin       = 60.0
	bottom       = pageH - margin
	bodySize     = 11.0
	leading      = 1.45
	indentStep   = 16.0
	cellPad      = 5.0
)

type font struct {
	name string
	ttf  []byte
}

var (
	regular = font{"regular", goregular.TTF}
	bold    = font{"bold", gobold.TTF}
	italic  = font{"italic", goitalic.TTF}
	mono    = font{"mono", gomono.TTF}
)

// stand-ins for characters the Go fonts don't have; others are left out
var substitutes = map[rune]string{'₦': "N"}

type Doc struct {
	Title  string
	Author string
	Text   string // Markdown; a first line repeating Title is left out
	At     time.Time
}

type writer struct {
	pdf    gopdf.GoPdf
	src    []byte
	page   int
	y      float64
	author string
	glyphs map[string]*sfnt.Font
}

func Render(d Doc) ([]byte, error) {
	body := strings.TrimSpace(d.Text)
	if first, rest, _ := strings.Cut(body, "\n"); strings.EqualFold(strings.Trim(first, " *#\"'"), strings.TrimSpace(d.Title)) {
		body = rest
	}
	w := &writer{src: []byte(body), author: d.Author, glyphs: map[string]*sfnt.Font{}}
	w.pdf.Start(gopdf.Config{PageSize: *gopdf.PageSizeA4, Unit: gopdf.UnitPT})
	w.pdf.SetInfo(gopdf.PdfInfo{Title: d.Title, Author: d.Author, Creator: "minime", CreationDate: d.At})
	for _, f := range []font{regular, bold, italic, mono} {
		if err := w.pdf.AddTTFFontData(f.name, f.ttf); err != nil {
			return nil, fmt.Errorf("pdf: font %s: %w", f.name, err)
		}
		g, err := sfnt.Parse(f.ttf)
		if err != nil {
			return nil, fmt.Errorf("pdf: font %s: %w", f.name, err)
		}
		w.glyphs[f.name] = g
	}
	w.newPage()
	if t := strings.TrimSpace(d.Title); t != "" {
		if err := w.lines(t, bold, 20, margin, pageW-2*margin, 0); err != nil {
			return nil, err
		}
		w.y += 6
		w.rule(margin)
		w.y += 14
	}
	md := goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough))
	root := md.Parser().Parse(text.NewReader(w.src))
	for n := root.FirstChild(); n != nil; n = n.NextSibling() {
		if err := w.block(n, margin, pageW-margin); err != nil {
			return nil, err
		}
	}
	var buf bytes.Buffer
	if _, err := w.pdf.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (w *writer) newPage() {
	w.pdf.AddPage()
	w.page++
	w.y = margin
	w.pdf.SetFont(regular.name, "", 8)
	w.pdf.SetTextColor(140, 140, 140)
	footer := fmt.Sprintf("%d", w.page)
	if w.author != "" {
		footer = w.author + "  ·  " + footer
	}
	footer = w.clean(footer, regular)
	if fw, err := w.pdf.MeasureTextWidth(footer); err == nil {
		w.pdf.SetXY(pageW-margin-fw, pageH-margin/2)
		w.pdf.Text(footer)
	}
	w.pdf.SetTextColor(20, 20, 20)
}

// room starts a new page unless h more points fit on this one.
func (w *writer) room(h float64) {
	if w.y+h > bottom {
		w.newPage()
	}
}

func (w *writer) rule(left float64) {
	w.pdf.SetStrokeColor(200, 200, 200)
	w.pdf.SetLineWidth(0.6)
	w.pdf.Line(left, w.y, pageW-margin, w.y)
}

func (w *writer) lines(s string, f font, size, x, width float64, grey uint8) error {
	s = w.clean(s, f)
	if strings.TrimSpace(s) == "" {
		return nil
	}
	if err := w.pdf.SetFont(f.name, "", size); err != nil {
		return err
	}
	var all []string
	for _, para := range strings.Split(s, "\n") {
		if strings.TrimSpace(para) == "" {
			all = append(all, "")
			continue
		}
		ls, err := w.pdf.SplitTextWithWordWrap(para, width)
		if err != nil {
			return err
		}
		all = append(all, ls...)
	}
	h := size * leading
	for _, l := range all {
		w.room(h)
		w.pdf.SetFont(f.name, "", size)
		w.pdf.SetTextColor(20+grey, 20+grey, 20+grey)
		w.pdf.SetXY(x, w.y+size)
		if err := w.pdf.Text(strings.TrimRight(l, " ")); err != nil {
			return err
		}
		w.y += h
	}
	w.pdf.SetTextColor(20, 20, 20)
	return nil
}

func (w *writer) block(n ast.Node, left, right float64) error {
	width := right - left
	switch n := n.(type) {
	case *ast.Heading:
		size := map[int]float64{1: 17, 2: 14, 3: 12.5}[n.Level]
		if size == 0 {
			size = bodySize + 0.5
		}
		w.y += size * 0.6
		w.room(size * leading * 2) // not alone at the foot of a page
		if err := w.lines(w.inline(n), bold, size, left, width, 0); err != nil {
			return err
		}
		w.y += 3
	case *ast.Paragraph, *ast.TextBlock:
		if err := w.lines(w.inline(n), regular, bodySize, left, width, 0); err != nil {
			return err
		}
		if _, tight := n.(*ast.TextBlock); !tight {
			w.y += bodySize * 0.6
		}
	case *ast.List:
		num := n.Start
		for item := n.FirstChild(); item != nil; item = item.NextSibling() {
			mark := "•"
			if n.IsOrdered() {
				mark = fmt.Sprintf("%d.", num)
				num++
			}
			w.room(bodySize * leading)
			w.pdf.SetFont(regular.name, "", bodySize)
			w.pdf.SetXY(left+2, w.y+bodySize)
			w.pdf.Text(mark)
			for c := item.FirstChild(); c != nil; c = c.NextSibling() {
				if err := w.block(c, left+indentStep+4, right); err != nil {
					return err
				}
			}
		}
		w.y += bodySize * 0.4
	case *ast.Blockquote:
		top := w.y
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			if err := w.block(c, left+indentStep, right); err != nil {
				return err
			}
		}
		if w.y > top { // a bar beside it, on its last page
			w.pdf.SetStrokeColor(190, 190, 190)
			w.pdf.SetLineWidth(2)
			w.pdf.Line(left+4, max(top, margin), left+4, w.y-bodySize*0.6)
		}
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		var b strings.Builder
		ls := n.Lines()
		for i := 0; i < ls.Len(); i++ {
			seg := ls.At(i)
			b.Write(seg.Value(w.src))
		}
		if err := w.lines(strings.TrimRight(b.String(), "\n"), mono, bodySize-1.5, left+8, width-8, 50); err != nil {
			return err
		}
		w.y += bodySize * 0.6
	case *ast.ThematicBreak:
		w.y += 4
		w.room(10)
		w.rule(left)
		w.y += 12
	case *east.Table:
		return w.table(n, left, width)
	case *ast.HTMLBlock:
		// left out
	default:
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			if err := w.block(c, left, right); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *writer) table(t *east.Table, left, width float64) error {
	var rows [][]string
	for r := t.FirstChild(); r != nil; r = r.NextSibling() {
		var row []string
		for c := r.FirstChild(); c != nil; c = c.NextSibling() {
			row = append(row, w.clean(w.inline(c), regular))
		}
		rows = append(rows, row)
	}
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	if cols == 0 {
		return nil
	}
	colW := width / float64(cols)
	size := bodySize - 1
	h := size * leading
	for ri, r := range rows {
		f := regular
		if ri == 0 {
			f = bold
		}
		if err := w.pdf.SetFont(f.name, "", size); err != nil {
			return err
		}
		cells := make([][]string, cols)
		tallest := 1
		for ci := range cols {
			if ci < len(r) && strings.TrimSpace(r[ci]) != "" {
				ls, err := w.pdf.SplitTextWithWordWrap(r[ci], colW-2*cellPad)
				if err != nil {
					return err
				}
				cells[ci] = ls
			}
			tallest = max(tallest, len(cells[ci]))
		}
		rowH := float64(tallest)*h + 2*cellPad
		w.room(rowH)
		for ci, ls := range cells {
			for li, l := range ls {
				w.pdf.SetXY(left+float64(ci)*colW+cellPad, w.y+cellPad+float64(li)*h+size)
				if err := w.pdf.Text(strings.TrimRight(l, " ")); err != nil {
					return err
				}
			}
		}
		w.y += rowH
		w.pdf.SetStrokeColor(210, 210, 210)
		w.pdf.SetLineWidth(map[bool]float64{true: 0.9, false: 0.4}[ri == 0])
		w.pdf.Line(left, w.y, left+width, w.y)
	}
	w.y += bodySize
	return nil
}

func (w *writer) inline(n ast.Node) string {
	var b strings.Builder
	var walk func(ast.Node)
	walk = func(n ast.Node) {
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch c := c.(type) {
			case *ast.Text:
				b.Write(c.Segment.Value(w.src))
				if c.HardLineBreak() || c.SoftLineBreak() { // a line break in plain writing is meant
					b.WriteString("\n")
				}
			case *ast.String:
				b.Write(c.Value)
			case *ast.AutoLink:
				b.Write(c.URL(w.src))
			case *ast.RawHTML:
			default:
				walk(c)
			}
		}
	}
	walk(n)
	return b.String()
}

func (w *writer) clean(s string, f font) string {
	g := w.glyphs[f.name]
	var buf sfnt.Buffer
	var b strings.Builder
	for _, r := range s {
		switch i, err := g.GlyphIndex(&buf, r); {
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteRune(' ')
		case err == nil && i != 0:
			b.WriteRune(r)
		default:
			b.WriteString(substitutes[r])
		}
	}
	return b.String()
}

// FileName is title as a file name, like "clinic-outreach-plan.pdf".
func FileName(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		if b.Len() >= 60 {
			break
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			dash = true
			continue
		}
		if dash && b.Len() > 0 {
			b.WriteByte('-')
		}
		b.WriteRune(r)
		dash = false
	}
	if b.Len() == 0 {
		return "work.pdf"
	}
	return b.String() + ".pdf"
}
