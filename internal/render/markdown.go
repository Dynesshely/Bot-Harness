package render

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"regexp"
	"strings"
	"unicode"
)

// Markdown is the input of the markdown-to-PNG renderer. It supports a
// deliberately small, chat-oriented subset: headings, paragraphs, lists,
// fenced code, blockquotes, horizontal rules and pipe tables.
type Markdown struct {
	Title string
	Body  string
}

const (
	mdWidth    = 760 * scale
	mdPad      = 40 * scale
	mdMaxLines = 400
	mdGap      = 10 * scale
)

var (
	mdBg        = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	mdText      = color.RGBA{R: 0x1f, G: 0x29, B: 0x37, A: 0xff}
	mdMuted     = color.RGBA{R: 0x6b, G: 0x72, B: 0x80, A: 0xff}
	mdRule      = color.RGBA{R: 0xd1, G: 0xd5, B: 0xdb, A: 0xff}
	mdCodeBg    = color.RGBA{R: 0xf3, G: 0xf4, B: 0xf6, A: 0xff}
	mdCodeText  = color.RGBA{R: 0x11, G: 0x18, B: 0x27, A: 0xff}
	mdTitleText = color.RGBA{R: 0x0f, G: 0x17, B: 0x2a, A: 0xff}
)

type mdLine struct {
	text   string
	chain  *fontChain
	color  color.Color
	indent int
	ascent int
	height int
	bg     color.Color
	rule   bool
}

type mdBuild struct {
	lines []mdLine
}

func (b *mdBuild) add(text string, chain *fontChain, col color.Color, indent int) {
	if text == "" {
		return
	}
	ascent, lineH := chain.metrics()
	for _, s := range wrapText(chain, text, mdWidth-2*mdPad-indent) {
		b.lines = append(b.lines, mdLine{
			text: s, chain: chain, color: col, indent: indent,
			ascent: ascent, height: lineH,
		})
	}
}

func (b *mdBuild) gap() {
	if len(b.lines) == 0 {
		return
	}
	b.lines = append(b.lines, mdLine{height: mdGap})
}

var (
	mdHeading  = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	mdList     = regexp.MustCompile(`^([-*+]|\d+[.)])\s+(.*)$`)
	mdLink     = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	mdImage    = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	mdTableSep = regexp.MustCompile(`^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)*\|?\s*$`)
)

func mdInline(s string) string {
	s = mdImage.ReplaceAllString(s, "[图片 $1]")
	s = mdLink.ReplaceAllString(s, "$1")
	s = strings.NewReplacer("**", "", "__", "", "~~", "", "\x60", "", "*", "", "_", "").Replace(s)
	return strings.TrimSpace(s)
}

// RenderMarkdown renders a markdown document as a PNG.
func RenderMarkdown(md Markdown) ([]byte, error) {
	img, err := renderMarkdownImage(md)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("render: encode markdown png: %w", err)
	}
	return buf.Bytes(), nil
}

func renderMarkdownImage(md Markdown) (image.Image, error) {
	body, err := newFontChain(15 * scale)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	code, err := newFontChain(13 * scale)
	if err != nil {
		return nil, err
	}
	defer code.Close()
	h1, err := newFontChain(30 * scale)
	if err != nil {
		return nil, err
	}
	defer h1.Close()
	h2, err := newFontChain(24 * scale)
	if err != nil {
		return nil, err
	}
	defer h2.Close()
	h3, err := newFontChain(20 * scale)
	if err != nil {
		return nil, err
	}
	defer h3.Close()

	b := &mdBuild{}
	var para []string
	flush := func() {
		if len(para) == 0 {
			return
		}
		b.add(mdInline(strings.Join(para, " ")), body, mdText, 0)
		b.gap()
		para = nil
	}

	lines := strings.Split(strings.ReplaceAll(md.Body, "\r\n", "\n"), "\n")
	inFence := false
	var fence []string
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		trim := strings.TrimSpace(raw)
		if strings.HasPrefix(trim, "\u0060\u0060\u0060") {
			if inFence {
				for _, s := range fence {
					b.lines = append(b.lines, mdLine{
						text: s, chain: code, color: mdCodeText, indent: 12 * scale,
						ascent: mustMetrics(code), height: mustLineH(code), bg: mdCodeBg,
					})
				}
				b.gap()
				fence = nil
				inFence = false
			} else {
				flush()
				inFence = true
			}
			continue
		}
		if inFence {
			fence = append(fence, strings.TrimRight(raw, " \t"))
			continue
		}
		if trim == "" {
			flush()
			continue
		}
		if m := mdHeading.FindStringSubmatch(trim); m != nil {
			flush()
			level := len(m[1])
			chain := h3
			if level == 1 {
				chain = h1
			} else if level == 2 {
				chain = h2
			}
			b.add(mdInline(m[2]), chain, mdTitleText, 0)
			b.gap()
			continue
		}
		if trim == "---" || trim == "***" || trim == "___" {
			flush()
			b.lines = append(b.lines, mdLine{rule: true, height: 2 * scale})
			b.gap()
			continue
		}
		if strings.HasPrefix(trim, ">") {
			flush()
			b.add("\u2502 "+mdInline(strings.TrimSpace(strings.TrimPrefix(trim, ">"))), body, mdMuted, 6*scale)
			continue
		}
		if m := mdList.FindStringSubmatch(trim); m != nil {
			flush()
			prefix := "\u2022 "
			if len(m[1]) > 0 && unicode.IsDigit(rune(m[1][0])) {
				prefix = m[1] + " "
			}
			b.add(prefix+mdInline(m[2]), body, mdText, 6*scale)
			continue
		}
		if strings.Contains(trim, "|") && i+1 < len(lines) && mdTableSep.MatchString(lines[i+1]) {
			flush()
			var rows [][]string
			rows = append(rows, splitMDCells(trim))
			i += 2
			for ; i < len(lines); i++ {
				row := strings.TrimSpace(lines[i])
				if row == "" || !strings.Contains(row, "|") {
					i--
					break
				}
				rows = append(rows, splitMDCells(row))
			}
			b.addMDTable(rows, body, code)
			b.gap()
			continue
		}
		para = append(para, trim)
	}
	flush()
	if inFence {
		for _, s := range fence {
			b.lines = append(b.lines, mdLine{text: s, chain: code, color: mdCodeText, indent: 12 * scale, ascent: mustMetrics(code), height: mustLineH(code), bg: mdCodeBg})
		}
	}
	if len(b.lines) == 0 {
		return nil, fmt.Errorf("render: markdown body is empty")
	}
	if len(b.lines) > mdMaxLines {
		return nil, fmt.Errorf("render: markdown too long (%d lines, max %d)", len(b.lines), mdMaxLines)
	}

	height := 2 * mdPad
	for _, ln := range b.lines {
		height += ln.height
	}
	if strings.TrimSpace(md.Title) != "" {
		height += mustLineH(h2) + 2*mdGap
	}
	if height > 12000*scale {
		return nil, fmt.Errorf("render: markdown image too tall")
	}
	img := image.NewRGBA(image.Rect(0, 0, mdWidth, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{mdBg}, image.Point{}, draw.Src)

	y := mdPad
	if title := strings.TrimSpace(md.Title); title != "" {
		ascent, lineH := h2.metrics()
		h2.draw(img, truncateToWidth(h2, mdInline(title), mdWidth-2*mdPad), mdPad, y+ascent, mdTitleText)
		y += lineH + mdGap
		draw.Draw(img, image.Rect(mdPad, y, mdWidth-mdPad, y+scale), &image.Uniform{mdRule}, image.Point{}, draw.Src)
		y += mdGap
	}
	for _, ln := range b.lines {
		if ln.rule {
			draw.Draw(img, image.Rect(mdPad, y+ln.height/2, mdWidth-mdPad, y+ln.height/2+scale), &image.Uniform{mdRule}, image.Point{}, draw.Src)
			y += ln.height
			continue
		}
		if ln.bg != nil && ln.height > 0 {
			draw.Draw(img, image.Rect(mdPad, y, mdWidth-mdPad, y+ln.height), &image.Uniform{ln.bg}, image.Point{}, draw.Src)
		}
		if ln.chain != nil {
			ln.chain.draw(img, ln.text, mdPad+ln.indent, y+ln.ascent, ln.color)
		}
		y += ln.height
	}
	return img, nil
}

func (b *mdBuild) addMDTable(rows [][]string, body, code *fontChain) {
	if len(rows) == 0 {
		return
	}
	maxCols := 0
	for _, row := range rows {
		if len(row) > maxCols {
			maxCols = len(row)
		}
	}
	if maxCols == 0 {
		return
	}
	if maxCols > 6 {
		for _, row := range rows {
			b.add("| "+strings.Join(row, " | ")+" |", code, mdCodeText, 0)
		}
		return
	}
	widths := make([]int, maxCols)
	for _, row := range rows {
		for j := 0; j < maxCols && j < len(row); j++ {
			if w := body.measure(mdInline(row[j])); w > widths[j] {
				widths[j] = w
			}
		}
	}
	total := 0
	for _, w := range widths {
		total += w + 3*scale
	}
	if total > mdWidth-2*mdPad {
		scaleDown := float64(mdWidth-2*mdPad) / float64(total)
		for j := range widths {
			widths[j] = int(float64(widths[j]) * scaleDown)
		}
	}
	for i, row := range rows {
		var cells []string
		for j := 0; j < maxCols; j++ {
			cell := ""
			if j < len(row) {
				cell = mdInline(row[j])
			}
			cells = append(cells, padMD(cell, widths[j], body))
		}
		line := "| " + strings.Join(cells, " | ") + " |"
		b.add(line, code, mdCodeText, 0)
		if i == 0 {
			b.lines = append(b.lines, mdLine{rule: true, height: 2 * scale})
		}
	}
}

func splitMDCells(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	parts := strings.Split(s, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func padMD(s string, width int, chain *fontChain) string {
	for chain.measure(s) < width {
		s += " "
	}
	return s
}

func mustMetrics(c *fontChain) int {
	ascent, _ := c.metrics()
	return ascent
}

func mustLineH(c *fontChain) int {
	_, lineH := c.metrics()
	return lineH
}
