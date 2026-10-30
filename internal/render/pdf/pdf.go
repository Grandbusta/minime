// Package pdf lays a piece of writing out as an A4 PDF: its title, then its
// text read as Markdown (render/markdown).
package pdf

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/signintech/gopdf"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"

	"github.com/Grandbusta/minime/internal/render/markdown"
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

// substitutes stand in for characters the Go fonts can't draw; others are
// left out.
var substitutes = map[rune]string{'₦': "N"}

type Doc struct {
	Title  string
	Author string
	Text   string // Markdown; a first line repeating Title is left out
	At     time.Time
}

type writer struct {
	pdf    gopdf.GoPdf
	page   int
	y      float64
	author string
	glyphs map[string]*sfnt.Font
}

func Render(d Doc) ([]byte, error) {
	w := &writer{author: d.Author, glyphs: map[string]*sfnt.Font{}}
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
	var prev markdown.Block
	for _, b := range markdown.Blocks(d.Title, d.Text) {
		if (prev.Kind == markdown.Item || prev.Tight) && b.Kind != markdown.Item && !b.Tight { // a list ended
			w.y += bodySize * 0.4
		}
		if err := w.block(b); err != nil {
			return nil, err
		}
		prev = b
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

func (w *writer) block(b markdown.Block) error {
	left := margin + float64(b.Depth)*(indentStep+4)
	width := pageW - margin - left
	top := w.y
	switch b.Kind {
	case markdown.Heading:
		size := map[int]float64{1: 17, 2: 14, 3: 12.5}[b.Level]
		if size == 0 {
			size = bodySize + 0.5
		}
		w.y += size * 0.6
		w.room(size * leading * 2) // not alone at the foot of a page
		if err := w.lines(b.Text, bold, size, left, width, 0); err != nil {
			return err
		}
		w.y += 3
	case markdown.Paragraph:
		if err := w.lines(b.Text, regular, bodySize, left, width, 0); err != nil {
			return err
		}
		if !b.Tight {
			w.y += bodySize * 0.6
		}
	case markdown.Item:
		w.room(bodySize * leading)
		w.pdf.SetFont(regular.name, "", bodySize)
		w.pdf.SetXY(left+2, w.y+bodySize)
		w.pdf.Text(b.Mark)
		if err := w.lines(b.Text, regular, bodySize, left+indentStep+4, width-indentStep-4, 0); err != nil {
			return err
		}
		w.y += bodySize * 0.15
	case markdown.Code:
		if err := w.lines(b.Text, mono, bodySize-1.5, left+8, width-8, 50); err != nil {
			return err
		}
		w.y += bodySize * 0.6
	case markdown.Rule:
		w.y += 4
		w.room(10)
		w.rule(left)
		w.y += 12
	case markdown.Table:
		if err := w.table(b.Rows, left, width); err != nil {
			return err
		}
	}
	if b.Quote && w.y > top { // a bar beside it, on its last page
		w.pdf.SetStrokeColor(190, 190, 190)
		w.pdf.SetLineWidth(2)
		w.pdf.Line(left-indentStep, max(top, margin), left-indentStep, w.y-bodySize*0.6)
	}
	return nil
}

func (w *writer) table(rows [][]string, left, width float64) error {
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
				ls, err := w.pdf.SplitTextWithWordWrap(w.clean(r[ci], f), colW-2*cellPad)
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
