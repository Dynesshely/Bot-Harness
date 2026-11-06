package render

import (
	"image"
	"testing"
)

// Fancy Unicode nicknames from Mathematical Alphanumeric Symbols: the block is
// absent from Noto Sans CJK SC, so before the fallback chain these rendered as
// .notdef boxes.
const (
	mathNicknameBoldItalic = "𝐶𝑟𝑎𝑛𝑦𝑜𝑧𝑒𝑛"
	mathNicknameScript     = "𝓒𝓻𝓪𝓷𝔂𝓸𝔃𝓮𝓷"
)

func renderPNGWith(t *testing.T, tbl Table, chainFor chainFactory) []byte {
	t.Helper()
	data, err := renderPNG(tbl, chainFor)
	if err != nil {
		t.Fatalf("renderPNG: %v", err)
	}
	return data
}

// chainWithoutMath drops NotoSansMath from the chain, which is what the
// renderer looked like before this change for the nickname runes.
func chainWithoutMath(size float64) (*fontChain, error) {
	fonts, err := loadFonts()
	if err != nil {
		return nil, err
	}
	return newFontChainFrom(size, fonts[fontCJK], fonts[fontSymbols2])
}

func mathTable() Table {
	return Table{
		Title:   "群聊氛围速览",
		Headers: []string{"对象", "推测", "证据"},
		Rows: [][]string{
			{mathNicknameBoldItalic, "情绪积极", "在 10:02 连着发了三条消息。"},
			{mathNicknameScript, "略有疲惫", "凌晨还在回消息，句子变短。"},
		},
	}
}

// TestFontFallbackCoverage pins the fallback order for the decorative Unicode
// nicknames the CJK face cannot draw: every rune must resolve to a secondary
// face (index 2 = NotoSansMath; index 1 is NotoEmoji), and the primary face
// must report .notdef (GlyphIndex == 0) for exactly those runes.
func TestFontFallbackCoverage(t *testing.T) {
	fonts, err := loadFonts()
	if err != nil {
		t.Fatalf("loadFonts: %v", err)
	}
	if len(fonts) != fontFamilyCount {
		t.Fatalf("chain has %d fonts, want %d", len(fonts), fontFamilyCount)
	}

	chain, err := newFontChain(bodyFontSize)
	if err != nil {
		t.Fatalf("newFontChain: %v", err)
	}
	defer chain.Close()

	for _, nickname := range []string{mathNicknameBoldItalic, mathNicknameScript} {
		for _, r := range nickname {
			if gi := glyphIndex(fonts[fontCJK], r); gi != 0 {
				t.Errorf("%q (U+%04X): primary CJK face has glyph %d, want .notdef (0)", string(r), r, gi)
			}
			if got := chain.selectedFontIndex(r); got == fontCJK {
				t.Errorf("%q (U+%04X): selected font index = 0 (primary), want fallback", string(r), r)
			} else if got != fontMath {
				t.Errorf("%q (U+%04X): selected font index = %d, want %d (NotoSansMath)", string(r), r, got, fontMath)
			}
		}
		t.Logf("%q: all %d runes fall back to index %d", nickname, len([]rune(nickname)), fontMath)
	}

	// The primary face still owns the rest of the string, including CJK.
	for _, r := range "对象推测证据" {
		if got := chain.selectedFontIndex(r); got != fontCJK {
			t.Errorf("%q (U+%04X): selected font index = %d, want 0 (CJK)", string(r), r, got)
		}
	}

	// The chain must measure the nickname at a real, non-zero width.
	if w := chain.measure(mathNicknameBoldItalic); w <= 0 {
		t.Errorf("measured width of %q = %d, want > 0", mathNicknameBoldItalic, w)
	} else {
		t.Logf("measured width: %q = %dpx, %q = %dpx",
			mathNicknameBoldItalic, w, mathNicknameScript, chain.measure(mathNicknameScript))
	}
}

// TestFancyNicknameFallsBackWhenRendering proves the fallback reaches the image:
// the same table renders differently with and without NotoSansMath in the
// chain, and all runes of the nickname pick a non-primary face when it is
// present.
func TestFancyNicknameFallsBackWhenRendering(t *testing.T) {
	withMath := decodePNG(t, renderPNGWith(t, mathTable(), newFontChain))
	without := decodePNG(t, renderPNGWith(t, mathTable(), chainWithoutMath))

	if withMath.Bounds().Dx() <= 0 || withMath.Bounds().Dy() <= 0 {
		t.Fatalf("with math: bad bounds %v", withMath.Bounds())
	}
	if without.Bounds().Dx() <= 0 || without.Bounds().Dy() <= 0 {
		t.Fatalf("without math: bad bounds %v", without.Bounds())
	}

	// Without the Math face the same runes resolve to the primary face, i.e.
	// they are drawn as .notdef boxes — precisely the bug this change fixes.
	noMathChain, err := chainWithoutMath(bodyFontSize)
	if err != nil {
		t.Fatalf("chainWithoutMath: %v", err)
	}
	defer noMathChain.Close()
	for _, r := range mathNicknameBoldItalic + mathNicknameScript {
		if got := noMathChain.selectedFontIndex(r); got != fontCJK {
			t.Errorf("%q (U+%04X): selected font index without Math = %d, want 0 (.notdef)", string(r), r, got)
		}
	}

	withRatio, withoutRatio := nonWhiteRatio(withMath), nonWhiteRatio(without)
	if withRatio <= 0.01 || withoutRatio <= 0.01 {
		t.Errorf("non-white ratio with=%.4f without=%.4f, want both > 1%%", withRatio, withoutRatio)
	}

	// The drawings must differ: identical pixels would mean the extra face
	// never reached the canvas. The geometry differs too — .notdef boxes are
	// wider than the real math glyphs, so the columns change size.
	overlapDiff, overlapTotal := differingPixels(withMath, without)
	widthDiffers := withMath.Bounds().Dx() != without.Bounds().Dx()
	if overlapDiff == 0 && !widthDiffers {
		t.Fatal("rendering with and without NotoSansMath produced identical images; fallback did not draw")
	}

	t.Logf("with math: %dx%d non-white=%.2f%%; without: %dx%d non-white=%.2f%%; width differs=%v, differing pixels in overlap=%d/%d",
		withMath.Bounds().Dx(), withMath.Bounds().Dy(), withRatio*100,
		without.Bounds().Dx(), without.Bounds().Dy(), withoutRatio*100, widthDiffers, overlapDiff, overlapTotal)
}

// TestSymbolFallbackRenders pins that symbol range runes (star, circled digit)
// neither crash nor measure to zero width. ★/→/① are covered by the CJK face;
// ⬛/☑/⛔ are emoji-presentation symbols now served by NotoEmoji (it sits before
// Symbols2), and ⌫/⌥/⏻ are Symbols2-only, so the rows exercise mixed-face runs.
func TestSymbolFallbackRenders(t *testing.T) {
	tbl := Table{
		Title:   "符号回退",
		Headers: []string{"符号", "覆盖"},
		Rows: [][]string{
			{"★ → ①", "星形、箭头、圈号"},
			{"⬛ ☑ ⛔", "Emoji 优先的符号区"},
			{"⌫ ⌥ ⏻", "Symbols2 补充区"},
		},
	}
	data := renderPNGWith(t, tbl, newFontChain)
	img := decodePNG(t, data)
	if b := img.Bounds(); b.Dx() <= 0 || b.Dy() <= 0 {
		t.Fatalf("bad bounds: %v", b)
	}
	if ratio := nonWhiteRatio(img); ratio <= 0.01 {
		t.Errorf("non-white ratio = %.4f, want > 1%%", ratio)
	}

	chain, err := newFontChain(bodyFontSize)
	if err != nil {
		t.Fatalf("newFontChain: %v", err)
	}
	defer chain.Close()

	for _, tc := range []struct {
		r    rune
		want int
		name string
	}{
		{'★', fontCJK, "CJK"},
		{'→', fontCJK, "CJK"},
		{'①', fontCJK, "CJK"},
		{'⬛', fontEmoji, "Emoji"},
		{'☑', fontEmoji, "Emoji"},
		{'⛔', fontEmoji, "Emoji"},
		{'⌫', fontSymbols2, "Symbols2"},
		{'⌥', fontSymbols2, "Symbols2"},
		{'⏻', fontSymbols2, "Symbols2"},
	} {
		if w := chain.measure(string(tc.r)); w <= 0 {
			t.Errorf("%q width = %d, want > 0", string(tc.r), w)
		}
		if got := chain.selectedFontIndex(tc.r); got != tc.want {
			t.Errorf("%q selected font index = %d, want %d (%s)", string(tc.r), got, tc.want, tc.name)
		}
	}
	t.Logf("sizes: %dx%d, png=%d bytes, non-white=%.2f%%",
		img.Bounds().Dx(), img.Bounds().Dy(), len(data), nonWhiteRatio(img)*100)
}

// TestEmojiHasGlyph replaces the old "emoji remains tofu" expectation:
// NotoEmoji is embedded as an outline (glyf/gvar) face, so x/image can
// rasterise it. The glyphs are monochrome outlines, not CBDT/COLR colour.
func TestEmojiHasGlyph(t *testing.T) {
	emojis := []rune{'😀', '🎉', '👍', '🔥', '❤'}
	fonts, err := loadFonts()
	if err != nil {
		t.Fatalf("loadFonts: %v", err)
	}
	if len(fonts) != fontFamilyCount {
		t.Fatalf("chain has %d fonts, want %d", len(fonts), fontFamilyCount)
	}

	chain, err := newFontChain(bodyFontSize)
	if err != nil {
		t.Fatalf("newFontChain: %v", err)
	}
	defer chain.Close()

	for _, r := range emojis {
		if gi := glyphIndex(fonts[fontCJK], r); gi != 0 {
			t.Errorf("CJK face unexpectedly covers %q (U+%04X) with glyph %d", string(r), r, gi)
		}
		if gi := glyphIndex(fonts[fontEmoji], r); gi == 0 {
			t.Errorf("emoji face has no glyph for %q (U+%04X)", string(r), r)
		}
		if got := chain.selectedFontIndex(r); got != fontEmoji {
			t.Errorf("%q (U+%04X): selected font index = %d, want %d (NotoEmoji)", string(r), r, got, fontEmoji)
		}
		if w := chain.measure(string(r)); w <= 0 {
			t.Errorf("%q measured width = %d, want > 0", string(r), w)
		}
	}

	tbl := Table{Title: "表情", Headers: []string{"表情", "说明"}, Rows: [][]string{
		{"😀 🎉 👍 🔥 ❤️", "轮廓单色 emoji（非彩色）"},
	}}
	img := decodePNG(t, renderPNGWith(t, tbl, newFontChain))
	if b := img.Bounds(); b.Dx() <= 0 || b.Dy() <= 0 {
		t.Fatalf("bad bounds: %v", b)
	}
	if ratio := nonWhiteRatio(img); ratio <= 0.01 {
		t.Errorf("non-white ratio = %.4f, want > 1%%", ratio)
	}
	t.Logf("emoji coverage: %d faces, sizes: %dx%d, non-white=%.2f%%",
		len(fonts), img.Bounds().Dx(), img.Bounds().Dy(), nonWhiteRatio(img)*100)
}

// TestEmojiFallback pins the emoji-specific fallback rules: single-code-point
// emoji select the Emoji face, VS16/ZWJ/skin-tone/keycap combiners are
// zero-width and invisible, and ZWJ sequences render without panicking. The
// latter stay as adjacent component glyphs because x/image has no GSUB
// shaping; they must remain readable rather than become tofu.
func TestEmojiFallback(t *testing.T) {
	fonts, err := loadFonts()
	if err != nil {
		t.Fatalf("loadFonts: %v", err)
	}
	chain, err := newFontChain(bodyFontSize)
	if err != nil {
		t.Fatalf("newFontChain: %v", err)
	}
	defer chain.Close()

	for _, r := range []rune{'😀', '🎉', '👍'} {
		if gi := glyphIndex(fonts[fontEmoji], r); gi == 0 {
			t.Errorf("emoji face has no glyph for %q (U+%04X)", string(r), r)
		}
		if got := chain.selectedFontIndex(r); got != fontEmoji {
			t.Errorf("%q (U+%04X): selected font index = %d, want %d (NotoEmoji)", string(r), r, got, fontEmoji)
		}
	}

	// Default-ignorable/combining runes must not advance the pen or draw a
	// standalone .notdef box.
	for _, r := range []rune{'\uFE0F', '\uFE0E', '\u200D', '\u200C', '\u20E3', '\U0001F3FB'} {
		if w := chain.measure(string(r)); w != 0 {
			t.Errorf("%q (U+%04X): measured width = %d, want 0", string(r), r, w)
		}
	}

	// ❤️ (U+2764 + VS16) must be pixel-identical to ❤: the selector is
	// invisible, not a tofu block.
	heartOnly := decodePNG(t, renderPNGWith(t, Table{
		Title: "VS16", Headers: []string{"表情"}, Rows: [][]string{{"\u2764"}},
	}, newFontChain))
	heartVS := decodePNG(t, renderPNGWith(t, Table{
		Title: "VS16", Headers: []string{"表情"}, Rows: [][]string{{"\u2764\uFE0F"}},
	}, newFontChain))
	if d, _ := differingPixels(heartOnly, heartVS); d != 0 {
		t.Errorf("❤ and ❤️ differ in %d pixels; VS16 should be zero-width/invisible", d)
	}
	if a, b := chain.measure("\u2764"), chain.measure("\u2764\uFE0F"); a != b {
		t.Errorf("measure(❤) = %d, measure(❤️) = %d, want equal", a, b)
	}

	// Composed sequences must not panic and must keep a real width. They are
	// drawn as adjacent component glyphs (no shaping), which is the accepted
	// readable fallback for the monochrome outline face.
	composed := []string{
		"\U0001F468\u200D\U0001F469\u200D\U0001F467", // 👨‍👩‍👧
		"\U0001F3F3\uFE0F\u200D\U0001F308",           // 🏳️‍🌈
		"\U0001F469\U0001F3FD\u200D\U0001F680",       // 👩🏽‍🚀
		"1\uFE0F\u20E3",                              // 1️⃣
		"\U0001F1E8\U0001F1F3",                       // 🇨🇳
	}
	tbl := Table{Title: "组合表情", Headers: []string{"序列", "说明"}}
	for i, s := range composed {
		if w := chain.measure(s); w <= 0 {
			t.Errorf("composed emoji #%d %q measured width = %d, want > 0", i, s, w)
		}
		tbl.Rows = append(tbl.Rows, []string{s, "组合序列"})
	}
	img := decodePNG(t, renderPNGWith(t, tbl, newFontChain))
	if b := img.Bounds(); b.Dx() <= 0 || b.Dy() <= 0 {
		t.Fatalf("composed emoji: bad bounds: %v", b)
	}
	if ratio := nonWhiteRatio(img); ratio <= 0.01 {
		t.Errorf("composed emoji non-white ratio = %.4f, want > 1%%", ratio)
	}
	t.Logf("composed emoji: %d sequences, %dx%d, non-white=%.2f%%",
		len(composed), img.Bounds().Dx(), img.Bounds().Dy(), nonWhiteRatio(img)*100)
}

// differingPixels counts, over the overlapping rectangle, the pixels whose
// RGBA values differ between two images, and returns that rectangle's area.
func differingPixels(a, b image.Image) (diff, total int) {
	overlap := a.Bounds().Intersect(b.Bounds())
	if overlap.Empty() {
		return 0, 0
	}
	for y := overlap.Min.Y; y < overlap.Max.Y; y++ {
		for x := overlap.Min.X; x < overlap.Max.X; x++ {
			total++
			ar, ag, abl, aa := a.At(x, y).RGBA()
			br, bg, bbl, ba := b.At(x, y).RGBA()
			if ar != br || ag != bg || abl != bbl || aa != ba {
				diff++
			}
		}
	}
	return diff, total
}

// TestFallbackChainIsolatesFaces guards the rune→face decision: a chain built
// with only the primary face must still produce a decodable PNG, and adding
// faces must never change which face is chosen for a rune the primary covers.
func TestFallbackChainIsolatesFaces(t *testing.T) {
	fonts, err := loadFonts()
	if err != nil {
		t.Fatalf("loadFonts: %v", err)
	}
	primaryOnly, err := newFontChainFrom(bodyFontSize, fonts[fontCJK])
	if err != nil {
		t.Fatalf("newFontChainFrom: %v", err)
	}
	defer primaryOnly.Close()

	full, err := newFontChain(bodyFontSize)
	if err != nil {
		t.Fatalf("newFontChain: %v", err)
	}
	defer full.Close()

	for _, r := range "对象ABC中★①" {
		if p, f := primaryOnly.selectedFontIndex(r), full.selectedFontIndex(r); p != f {
			t.Errorf("%q: primary-only index = %d, full chain index = %d; adding faces changed a covered rune", string(r), p, f)
		}
	}

	img := decodePNG(t, renderPNGWith(t, mathTable(), func(size float64) (*fontChain, error) {
		return newFontChainFrom(size, fonts[fontCJK])
	}))
	if b := img.Bounds(); b.Dx() <= 0 || b.Dy() <= 0 {
		t.Fatalf("primary-only bad bounds: %v", b)
	}
}
