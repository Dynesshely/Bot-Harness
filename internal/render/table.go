// Package render turns structured data into images for delivery through chat
// transports that do not render rich text.
package render

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"unicode"
)

// Table is the platform-independent input of the PNG renderer. It deliberately
// has no dependency on core or store: callers only hand over strings.
type Table struct {
	Title   string
	Headers []string
	Rows    [][]string
}

// The renderer works at 2x and lets the browser/QQ client downscale the PNG,
// which keeps CJK glyph edges and hairlines crisp on high-density screens.
const (
	scale = 2

	bodyFontSize   = 14.0 * scale
	titleFontSize  = 18.0 * scale
	footerFontSize = 12.0 * scale

	cellPadX = 10 * scale
	cellPadY = 6 * scale

	// maxColWidth is the maximum width of one cell, padding included.
	maxColWidth = 480 * scale

	maxRows = 40
)

var (
	bgColor     = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	titleColor  = color.RGBA{R: 0x11, G: 0x18, B: 0x27, A: 0xff}
	headerBg    = color.RGBA{R: 0x1f, G: 0x29, B: 0x37, A: 0xff}
	headerColor = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	bodyColor   = color.RGBA{R: 0x1f, G: 0x29, B: 0x37, A: 0xff}
	zebraColor  = color.RGBA{R: 0xf6, G: 0xf8, B: 0xfa, A: 0xff}
	gridColor   = color.RGBA{R: 0xd1, G: 0xd5, B: 0xdb, A: 0xff}
	footerColor = color.RGBA{R: 0x6b, G: 0x72, B: 0x80, A: 0xff}
)

// chainFactory builds a font fallback chain at the requested pixel size.
// Production uses newFontChain (CJK → Emoji → Math → Symbols2); tests inject a
// reduced chain to prove the fallback actually changes the rendered output.
type chainFactory func(size float64) (*fontChain, error)

// RenderPNG renders t as a PNG image.
func RenderPNG(t Table) ([]byte, error) {
	return renderPNG(t, newFontChain)
}

func renderPNG(t Table, chainFor chainFactory) ([]byte, error) {
	img, err := renderImage(t, chainFor)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("render: encode png: %w", err)
	}
	return buf.Bytes(), nil
}

// RenderImage renders t to an in-memory image. It is exported so callers and
// tests can inspect dimensions without round-tripping through PNG.
func RenderImage(t Table) (image.Image, error) {
	return renderImage(t, newFontChain)
}

func renderImage(t Table, chainFor chainFactory) (image.Image, error) {
	bodyChain, err := chainFor(bodyFontSize)
	if err != nil {
		return nil, err
	}
	defer bodyChain.Close()

	titleChain, err := chainFor(titleFontSize)
	if err != nil {
		return nil, err
	}
	defer titleChain.Close()

	footerChain, err := chainFor(footerFontSize)
	if err != nil {
		return nil, err
	}
	defer footerChain.Close()

	bodyAscent, bodyLineH := bodyChain.metrics()
	titleAscent, titleLineH := titleChain.metrics()
	footerAscent, footerLineH := footerChain.metrics()

	// Normalise the input: every row has exactly as many cells as the widest
	// of headers/rows, and at most maxRows body rows are displayed.
	rows := t.Rows
	totalRows := len(rows)
	truncated := false
	if totalRows > maxRows {
		rows = rows[:maxRows]
		truncated = true
	}
	emptyData := len(rows) == 0 || !rowsHaveContent(rows)

	headers := append([]string(nil), t.Headers...)
	cols := len(headers)
	for _, row := range rows {
		if len(row) > cols {
			cols = len(row)
		}
	}
	if cols == 0 {
		cols = 1
	}
	hasHeader := len(t.Headers) > 0
	if len(headers) < cols {
		headers = append(headers, make([]string, cols-len(headers))...)
	}

	normRows := make([][]string, len(rows))
	for i, row := range rows {
		normRows[i] = make([]string, cols)
		copy(normRows[i], row)
	}

	// Column widths follow the actual measured text. Every rune is measured
	// through the fallback chain, so a decorative Unicode nickname is just as
	// wide as the face that actually draws it.
	maxContentW := maxColWidth - 2*cellPadX
	minColW := bodyChain.measure("M")
	if w := bodyChain.measure("中"); w > minColW {
		minColW = w
	}
	colWidths := make([]int, cols)
	for i := 0; i < cols; i++ {
		if hasHeader {
			measureNaturalWidth(bodyChain, headers[i], &colWidths[i])
		}
		for _, row := range normRows {
			measureNaturalWidth(bodyChain, row[i], &colWidths[i])
		}
	}
	if emptyData {
		measureNaturalWidth(bodyChain, "无数据", &colWidths[0])
	}
	for i := range colWidths {
		if colWidths[i] > maxContentW {
			colWidths[i] = maxContentW
		}
		if colWidths[i] < minColW {
			colWidths[i] = minColW
		}
	}

	headerLines := make([][]string, cols)
	if hasHeader {
		for i := 0; i < cols; i++ {
			headerLines[i] = wrapText(bodyChain, headers[i], colWidths[i])
		}
	}

	type bodyRow struct {
		lines  [][]string
		height int
		merged bool
	}
	bodyRows := make([]bodyRow, 0, len(normRows)+1)
	if emptyData {
		bodyRows = append(bodyRows, bodyRow{
			lines:  [][]string{{"无数据"}},
			height: bodyLineH + 2*cellPadY,
			merged: true,
		})
	} else {
		for _, row := range normRows {
			lines := make([][]string, cols)
			maxLines := 1
			for i := 0; i < cols; i++ {
				lines[i] = wrapText(bodyChain, row[i], colWidths[i])
				if len(lines[i]) > maxLines {
					maxLines = len(lines[i])
				}
			}
			bodyRows = append(bodyRows, bodyRow{
				lines:  lines,
				height: maxLines*bodyLineH + 2*cellPadY,
			})
		}
	}

	lineW := scale
	width := lineW
	for _, w := range colWidths {
		width += w + 2*cellPadX + lineW
	}

	// The title is always one line. It is truncated to the table width rather
	// than widening the image.
	title := strings.Join(strings.Fields(t.Title), " ")
	if title != "" {
		title = truncateToWidth(titleChain, title, width-2*(lineW+cellPadX))
	}
	titleH := 0
	if title != "" {
		titleH = titleLineH + 2*cellPadY
	}

	headerH := 0
	if hasHeader {
		maxLines := 1
		for _, lines := range headerLines {
			if len(lines) > maxLines {
				maxLines = len(lines)
			}
		}
		headerH = maxLines*bodyLineH + 2*cellPadY
	}

	tableTop := titleH
	tableBottom := tableTop + lineW
	if hasHeader {
		tableBottom += headerH + lineW
	}
	for _, row := range bodyRows {
		tableBottom += row.height + lineW
	}

	footerH := 0
	if truncated {
		footerH = footerLineH + 2*cellPadY
	}
	height := tableBottom + footerH
	if height < 1 {
		height = 1
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	fillRect(img, img.Bounds(), bgColor)

	if title != "" {
		titleChain.draw(img, title, lineW+cellPadX, cellPadY+titleAscent, titleColor)
	}

	// Background bands: header first, then zebra rows.
	if hasHeader {
		fillRect(img, image.Rect(0, tableTop+lineW, width, tableTop+lineW+headerH), headerBg)
	}
	y := tableTop + lineW
	if hasHeader {
		y += headerH + lineW
	}
	for i, row := range bodyRows {
		if i%2 == 1 {
			fillRect(img, image.Rect(0, y, width, y+row.height), zebraColor)
		}
		y += row.height + lineW
	}

	// Outer border.
	fillRect(img, image.Rect(0, tableTop, width, tableTop+lineW), gridColor)
	fillRect(img, image.Rect(0, tableBottom-lineW, width, tableBottom), gridColor)
	fillRect(img, image.Rect(0, tableTop, lineW, tableBottom), gridColor)
	fillRect(img, image.Rect(width-lineW, tableTop, width, tableBottom), gridColor)

	// Header text and separators.
	if hasHeader {
		y := tableTop + lineW
		for i := 0; i < cols; i++ {
			x := cellX(colWidths, i)
			for k, line := range headerLines[i] {
				bodyChain.draw(img, line, x+cellPadX,
					y+cellPadY+bodyAscent+k*bodyLineH, headerColor)
			}
		}
		for i := 0; i < cols-1; i++ {
			x := cellX(colWidths, i) + colWidths[i] + 2*cellPadX
			fillRect(img, image.Rect(x, y, x+lineW, y+headerH), gridColor)
		}
	}

	// Body rows, separators, and zebra-aware text.
	y = tableTop + lineW
	if hasHeader {
		y += headerH + lineW
	}
	for _, row := range bodyRows {
		if !row.merged {
			for i := 0; i < cols-1; i++ {
				x := cellX(colWidths, i) + colWidths[i] + 2*cellPadX
				fillRect(img, image.Rect(x, y, x+lineW, y+row.height), gridColor)
			}
			for i := 0; i < cols; i++ {
				x := cellX(colWidths, i)
				for k, line := range row.lines[i] {
					bodyChain.draw(img, line, x+cellPadX,
						y+cellPadY+bodyAscent+k*bodyLineH, bodyColor)
				}
			}
		} else {
			bodyChain.draw(img, "无数据", lineW+cellPadX,
				y+cellPadY+bodyAscent, bodyColor)
		}
		// Horizontal line below this row.
		fillRect(img, image.Rect(0, y+row.height, width, y+row.height+lineW), gridColor)
		y += row.height + lineW
	}

	if truncated {
		note := fmt.Sprintf("…（共 %d 行）", totalRows)
		footerChain.draw(img, note, lineW+cellPadX,
			tableBottom+cellPadY+footerAscent, footerColor)
	}

	return img, nil
}

func rowsHaveContent(rows [][]string) bool {
	for _, row := range rows {
		for _, cell := range row {
			if strings.TrimSpace(cell) != "" {
				return true
			}
		}
	}
	return false
}

func cellX(colWidths []int, col int) int {
	x := scale
	for i := 0; i < col; i++ {
		x += colWidths[i] + 2*cellPadX + scale
	}
	return x
}

func measureNaturalWidth(chain *fontChain, text string, width *int) {
	for _, line := range strings.Split(text, "\n") {
		if w := chain.measure(line); w > *width {
			*width = w
		}
	}
}

// wrapText greedily wraps s to maxWidth pixels. It breaks after spaces and
// common punctuation when possible and otherwise falls back to rune-level
// breaking so CJK text never overflows its column. Every width comes from the
// fallback chain's rune-aware measurement.
func wrapText(chain *fontChain, s string, maxWidth int) []string {
	if maxWidth < 1 {
		maxWidth = 1
	}

	var lines []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			lines = append(lines, "")
			continue
		}

		var cur []rune
		lastBreak := -1
		for _, r := range para {
			if chain.measure(string(cur)+string(r)) <= maxWidth || len(cur) == 0 {
				cur = append(cur, r)
				if breakOpportunity(r) {
					lastBreak = len(cur)
				}
				continue
			}

			// The new rune does not fit. Prefer the last break opportunity if
			// it leaves something on both sides of the split.
			if lastBreak > 0 && lastBreak < len(cur) {
				head := append([]rune(nil), cur[:lastBreak]...)
				rest := append([]rune(nil), cur[lastBreak:]...)
				lines = append(lines, strings.TrimRight(string(head), " 	"))
				for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '	') {
					rest = rest[1:]
				}
				cur = rest
			} else {
				lines = append(lines, strings.TrimRight(string(cur), " 	"))
				cur = cur[:0]
			}

			lastBreak = -1
			for i, rr := range cur {
				if breakOpportunity(rr) {
					lastBreak = i + 1
				}
			}
			if len(cur) > 0 && chain.measure(string(cur)+string(r)) > maxWidth {
				lines = append(lines, strings.TrimRight(string(cur), " 	"))
				cur = cur[:0]
				lastBreak = -1
			}
			cur = append(cur, r)
			if breakOpportunity(r) {
				lastBreak = len(cur)
			}
		}
		if len(cur) > 0 {
			lines = append(lines, strings.TrimRight(string(cur), " 	"))
		}
	}

	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines
}

func breakOpportunity(r rune) bool {
	switch r {
	case ' ', '\t', '-', '/', '\\', ',', '.', ';', ':', '!', '?',
		'、', '。', '，', '；', '：', '！', '？', '（', '）', '【', '】',
		'「', '」', '『', '』', '《', '》', '—', '·':
		return true
	}
	return unicode.IsSpace(r)
}

func truncateToWidth(chain *fontChain, s string, maxWidth int) string {
	if maxWidth <= 0 || s == "" {
		return ""
	}
	if chain.measure(s) <= maxWidth {
		return s
	}
	const ellipsis = "…"
	ellipsisW := chain.measure(ellipsis)
	if ellipsisW > maxWidth {
		return ellipsis
	}

	var b strings.Builder
	for _, r := range s {
		if chain.measure(b.String()+string(r)+ellipsis) > maxWidth {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimRight(b.String(), " 	") + ellipsis
}

func fillRect(dst *image.RGBA, r image.Rectangle, c color.Color) {
	draw.Draw(dst, r, image.NewUniform(c), image.Point{}, draw.Src)
}
