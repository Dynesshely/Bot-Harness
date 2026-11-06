package render

import (
	"bytes"
	"fmt"
	"image"
	_ "image/png"
	"os"
	"strings"
	"testing"
)

func sampleTable() Table {
	return Table{
		Title:   "群聊氛围速览",
		Headers: []string{"对象", "推测", "证据"},
		Rows: [][]string{
			{"小明", "情绪积极，参与度高", "他在 10:02 连续发了三条消息，提到「周末去爬山」并 @ 了两个人，语气轻快。"},
			{"Alice", "Neutral / quiet", "Last seen around 21:40; replied with a short \"ok\" after a long silence."},
			{"张三", "略有疲惫", "凌晨还在回消息，句子变短，错别字增多，例如把「今天」打成「今太」。"},
		},
	}
}

func decodePNG(t *testing.T, data []byte) image.Image {
	t.Helper()
	if !bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		t.Fatalf("output is not a PNG: prefix % x", data[:min(8, len(data))])
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("image.Decode: %v", err)
	}
	if format != "png" {
		t.Fatalf("decoded format = %q, want png", format)
	}
	return img
}

func nonWhiteRatio(img image.Image) float64 {
	b := img.Bounds()
	var nonWhite, total int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			total++
			if r>>8 < 0xf8 || g>>8 < 0xf8 || bl>>8 < 0xf8 {
				nonWhite++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(nonWhite) / float64(total)
}

func TestRenderPNGChineseEnglishAndWrap(t *testing.T) {
	data, err := RenderPNG(sampleTable())
	if err != nil {
		t.Fatalf("RenderPNG: %v", err)
	}
	if p := os.Getenv("RENDER_SAMPLE_PATH"); p != "" {
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatalf("write sample: %v", err)
		}
	}

	img := decodePNG(t, data)
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		t.Fatalf("bad bounds: %v", b)
	}

	direct, err := RenderImage(sampleTable())
	if err != nil {
		t.Fatalf("RenderImage: %v", err)
	}
	if direct.Bounds() != b {
		t.Errorf("RenderImage bounds = %v, decoded PNG bounds = %v", direct.Bounds(), b)
	}
	// The table is rendered at 2x; a three-column table must be comfortably
	// wider than a phone screenshot but not unbounded.
	if b.Dx() < 400 || b.Dx() > 5000 {
		t.Errorf("width = %d, want 400..5000", b.Dx())
	}
	if b.Dy() < 200 || b.Dy() > 5000 {
		t.Errorf("height = %d, want 200..5000", b.Dy())
	}
	if ratio := nonWhiteRatio(img); ratio <= 0.01 {
		t.Errorf("non-white pixel ratio = %.4f, want > 1%%", ratio)
	}
	t.Logf("sample: %dx%d, png=%d bytes, non-white=%.2f%%",
		b.Dx(), b.Dy(), len(data), nonWhiteRatio(img)*100)
}

func TestWrapLongEvidence(t *testing.T) {
	chain, err := newFontChain(bodyFontSize)
	if err != nil {
		t.Fatalf("newFontChain: %v", err)
	}
	defer chain.Close()

	text := strings.Repeat("这是一段很长的证据，用来验证按内容自动换行；", 8) +
		"English words should also wrap at spaces without overflowing the column."
	const width = 240
	lines := wrapText(chain, text, width)
	if len(lines) < 2 {
		t.Fatalf("wrapText returned %d line(s), want multiple", len(lines))
	}
	for i, line := range lines {
		if line == "" {
			t.Errorf("line %d is empty", i)
			continue
		}
		if got := chain.measure(line); got > width {
			t.Errorf("line %d width = %d, want <= %d (%q)", i, got, width, line)
		}
	}
	// Wrapping must not drop content: joining CJK lines and removing breaks at
	// spaces restores the original text.
	joined := strings.Join(lines, "")
	joined = strings.ReplaceAll(joined, " ", "")
	want := strings.ReplaceAll(text, " ", "")
	if joined != want {
		t.Errorf("wrapped text lost content: got %q, want %q", joined, want)
	}
}

func TestEmptyTable(t *testing.T) {
	cases := []struct {
		name string
		tbl  Table
	}{
		{"headers-no-rows", Table{Title: "空表", Headers: []string{"列 A", "列 B"}}},
		{"empty-row", Table{Title: "空行", Headers: []string{"列 A", "列 B"}, Rows: [][]string{{"", ""}}}},
		{"ragged-row", Table{Headers: []string{"列 A"}, Rows: [][]string{{"只有一列", "多出来的列"}}}},
		{"completely-empty", Table{}},
		{"title-only", Table{Title: "只有标题"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := RenderPNG(tc.tbl)
			if err != nil {
				t.Fatalf("RenderPNG: %v", err)
			}
			img := decodePNG(t, data)
			b := img.Bounds()
			if b.Dx() <= 0 || b.Dy() <= 0 {
				t.Fatalf("bad bounds: %v", b)
			}
			t.Logf("%s: %dx%d, png=%d bytes", tc.name, b.Dx(), b.Dy(), len(data))
		})
	}
}

func TestLongTitleIsSingleLine(t *testing.T) {
	short := sampleTable()
	short.Title = "短标题"

	long := sampleTable()
	long.Title = strings.Repeat("这是一个非常长的标题", 40)

	shortData, err := RenderPNG(short)
	if err != nil {
		t.Fatalf("short title: %v", err)
	}
	longData, err := RenderPNG(long)
	if err != nil {
		t.Fatalf("long title: %v", err)
	}
	shortImg, longImg := decodePNG(t, shortData), decodePNG(t, longData)

	if shortImg.Bounds().Dx() != longImg.Bounds().Dx() {
		t.Errorf("long title widened the image: short=%d long=%d",
			shortImg.Bounds().Dx(), longImg.Bounds().Dx())
	}
	if shortImg.Bounds().Dy() != longImg.Bounds().Dy() {
		t.Errorf("long title changed the height: short=%d long=%d",
			shortImg.Bounds().Dy(), longImg.Bounds().Dy())
	}
}

func TestTruncatesOver40Rows(t *testing.T) {
	makeTable := func(rows int) Table {
		t := Table{Title: "大表", Headers: []string{"序号", "内容"}}
		for i := 0; i < rows; i++ {
			t.Rows = append(t.Rows, []string{fmt.Sprintf("%d", i+1), fmt.Sprintf("第 %d 行", i+1)})
		}
		return t
	}
	height := func(rows int) int {
		data, err := RenderPNG(makeTable(rows))
		if err != nil {
			t.Fatalf("RenderPNG(%d rows): %v", rows, err)
		}
		return decodePNG(t, data).Bounds().Dy()
	}

	h1, h2, h40, h45 := height(1), height(2), height(maxRows), height(maxRows+5)
	rowH := h2 - h1
	if rowH <= 0 {
		t.Fatalf("row height = %d, want > 0", rowH)
	}
	if h45 <= h40 {
		t.Errorf("45-row image height = %d, want > 40-row height %d (footer note)", h45, h40)
	}
	// If truncation were broken the 5 extra rows would add 5*rowH. With
	// truncation only the footer line is added.
	if extra := h45 - h40; extra >= 3*rowH {
		t.Errorf("height grew by %d for 5 hidden rows (rowH=%d), truncation appears broken", extra, rowH)
	}
	t.Logf("rowH=%d, h40=%d, h45=%d, footerDelta=%d", rowH, h40, h45, h45-h40)
}

func TestCJKWidthGreaterThanASCII(t *testing.T) {
	chain, err := newFontChain(bodyFontSize)
	if err != nil {
		t.Fatalf("newFontChain: %v", err)
	}
	defer chain.Close()

	cjk := chain.measure(strings.Repeat("中", 10))
	ascii := chain.measure(strings.Repeat("a", 10))
	if cjk <= ascii {
		t.Errorf("CJK width = %d, ASCII width = %d; want CJK greater", cjk, ascii)
	}
	t.Logf("10 runes: CJK=%dpx ASCII=%dpx (ratio %.2f)", cjk, ascii, float64(cjk)/float64(ascii))
}

func BenchmarkRenderPNG(b *testing.B) {
	tbl := sampleTable()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := RenderPNG(tbl); err != nil {
			b.Fatal(err)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
