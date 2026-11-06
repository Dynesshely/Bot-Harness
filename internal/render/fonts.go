package render

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Font family indices in the fallback chain. The order is significant: the
// first face whose GlyphIndex is non-zero wins, so CJK/Latin and the common
// symbols the primary face already covers keep using it.
const (
	fontCJK = iota
	fontEmoji
	fontMath
	fontSymbols2
	fontFamilyCount
)

// fontFamilyNames is used in errors and must stay aligned with embeddedFonts.
var fontFamilyNames = [fontFamilyCount]string{
	"NotoSansCJKsc-Regular.otf",
	"NotoEmoji-Variable.ttf",
	"NotoSansMath-Regular.ttf",
	"NotoSansSymbols2-Regular.ttf",
}

// embeddedFonts is the fallback chain, in priority order. Emoji sits before
// Math/Symbols2 on purpose: NotoEmoji covers the emoji-presentation ranges
// (U+1F300…, U+2600…, U+2B00…) that Symbols2 also has monochrome glyphs for,
// and the emoji design is the more recognisable one there.
var embeddedFonts = [fontFamilyCount][]byte{fontFile, emojiFontFile, mathFontFile, symbolsFontFile}

// glyphIndex returns the glyph id for r, or 0 (.notdef) when the face does not
// cover it. A nil buffer makes sfnt use a temporary one, so the call is safe
// from any goroutine.
func glyphIndex(f *opentype.Font, r rune) sfnt.GlyphIndex {
	gi, err := f.GlyphIndex(nil, r)
	if err != nil {
		return 0
	}
	return gi
}

var (
	fontsOnce   sync.Once
	fontsParsed []*opentype.Font
	fontsErr    error
)

// loadFonts parses the embedded chain once and returns it in priority order.
// The parsed fonts are safe for concurrent use.
func loadFonts() ([]*opentype.Font, error) {
	fontsOnce.Do(func() {
		parsed := make([]*opentype.Font, 0, fontFamilyCount)
		for i, data := range embeddedFonts {
			f, err := opentype.Parse(data)
			if err != nil {
				fontsErr = fmt.Errorf("render: parse embedded font %s: %w", fontFamilyNames[i], err)
				return
			}
			parsed = append(parsed, f)
		}
		fontsParsed = parsed
	})
	if fontsErr != nil {
		return nil, fontsErr
	}
	return fontsParsed, nil
}

// runeMetric is the resolved fallback decision for one rune: which face in the
// chain draws it and how wide its advance is at this chain's size. skip marks
// default-ignorable code points (variation selectors, ZWJ, emoji modifiers …)
// that are zero-width and must not be rasterised as a standalone .notdef.
type runeMetric struct {
	index   int
	advance fixed.Int26_6
	skip    bool
}

// isZeroWidth reports whether r is a default-ignorable/combining code point
// that must neither advance the pen nor be drawn on its own.
//
// VS15/VS16 (U+FE0E/U+FE0F) select text/emoji presentation, ZWJ/ZWNJ/BOM and
// the bidi controls are formatting characters, and U+E0020–U+E007F are the tag
// characters used by subdivision flags. Emoji skin-tone modifiers (U+1F3FB–
// U+1F3FF) and U+20E3 (combining enclosing keycap) need GSUB shaping to compose
// with their base; x/image has no shaping engine, so drawing them standalone
// would add a stray glyph. Dropping them keeps the base emoji readable.
func isZeroWidth(r rune) bool {
	switch {
	case r == '\u200B' || r == '\u200C' || r == '\u200D' || r == '\u200E' || r == '\u200F':
		return true
	case r == '\u202A' || r == '\u202B' || r == '\u202C' || r == '\u202D' || r == '\u202E':
		return true
	case r == '\u2060' || r == '\u2061' || r == '\u2062' || r == '\u2063' || r == '\u2064':
		return true
	case r >= '\uFE00' && r <= '\uFE0F':
		return true
	case r == '\uFEFF' || r == '\u20E3':
		return true
	case r >= '\U000E0020' && r <= '\U000E007F':
		return true
	case r >= '\U0001F3FB' && r <= '\U0001F3FF':
		return true
	}
	return false
}

// fontChain is the fallback chain materialised as faces at one font size.
//
// A chain is not safe for concurrent use: it holds an sfnt.Buffer and the faces
// cache glyph masks. RenderImage builds one chain per size per call.
type fontChain struct {
	fonts []*opentype.Font
	faces []font.Face
	cache map[rune]runeMetric
}

// newFontChain builds the production chain [CJK, Emoji, Math, Symbols2] at
// size.
func newFontChain(size float64) (*fontChain, error) {
	fonts, err := loadFonts()
	if err != nil {
		return nil, err
	}
	return newFontChainFrom(size, fonts...)
}

// newFontChainFrom builds a chain from an explicit, ordered subset. The first
// entry is the primary face and supplies the vertical metrics and the tofu
// fallback; the rest are consulted in order. Tests use this to render with a
// face removed and prove the fallback actually changes the output.
func newFontChainFrom(size float64, fonts ...*opentype.Font) (*fontChain, error) {
	if len(fonts) == 0 {
		return nil, fmt.Errorf("render: empty font chain")
	}
	c := &fontChain{
		fonts: append([]*opentype.Font(nil), fonts...),
		faces: make([]font.Face, len(fonts)),
		cache: make(map[rune]runeMetric, 256),
	}
	for i, f := range fonts {
		face, err := opentype.NewFace(f, &opentype.FaceOptions{
			Size:    size,
			DPI:     72,
			Hinting: font.HintingNone,
		})
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("render: create font face %d: %w", i, err)
		}
		c.faces[i] = face
	}
	return c, nil
}

// Close releases the faces. The parsed fonts themselves stay cached.
func (c *fontChain) Close() {
	for _, f := range c.faces {
		if f != nil {
			_ = f.Close()
		}
	}
	c.faces = nil
	c.fonts = nil
	c.cache = nil
}

// rune resolves r against the chain, caching the decision. A rune no face
// covers resolves to the primary face, which then draws .notdef — the same
// tofu behaviour the renderer had before the fallback chain existed.
func (c *fontChain) rune(r rune) runeMetric {
	if m, ok := c.cache[r]; ok {
		return m
	}
	if isZeroWidth(r) {
		m := runeMetric{index: 0, skip: true}
		c.cache[r] = m
		return m
	}
	m := runeMetric{index: 0}
	for i, f := range c.fonts {
		if gi := glyphIndex(f, r); gi != 0 {
			m.index = i
			break
		}
	}
	adv, _ := c.faces[m.index].GlyphAdvance(r)
	m.advance = adv
	c.cache[r] = m
	return m
}

// selectedFontIndex reports which face in the chain draws r: 0 = primary
// (Noto Sans CJK SC), 1 = Noto Emoji, 2 = Noto Sans Math, 3 = Noto Sans
// Symbols2 for the production chain. Exposed so tests can pin the fallback
// order. Zero-width runes report 0 and are never drawn.
func (c *fontChain) selectedFontIndex(r rune) int {
	return c.rune(r).index
}

// measure is the single rune-aware measurement used for column widths,
// wrapping and truncation: sum the advance of the face each rune resolves to.
// Kerning is ignored on purpose so measurement and drawing agree (the drawer
// below disables it too).
func (c *fontChain) measure(s string) int {
	var adv fixed.Int26_6
	for _, r := range s {
		adv += c.rune(r).advance
	}
	return int(math.Ceil(float64(adv) / 64.0))
}

// metrics reports the ascent and line height of the primary face. Secondary
// faces render at the same size on the same baseline, so CJK metrics define the
// row geometry for the whole chain.
func (c *fontChain) metrics() (ascent, lineH int) {
	m := c.faces[0].Metrics()
	ascent = m.Ascent.Ceil()
	lineH = (m.Ascent + m.Descent).Ceil() + scale
	return ascent, lineH
}

// noKernFace disables kerning so font.Drawer advances exactly by the sum of
// glyph advances the chain measured.
type noKernFace struct{ font.Face }

func (noKernFace) Kern(r0, r1 rune) fixed.Int26_6 { return 0 }

// draw renders s with the fallback chain: consecutive runes that resolve to the
// same face are grouped into a run and each run is drawn with a font.Drawer
// using that face. The caller supplies the baseline, which comes from the
// primary face's metrics for every run.
func (c *fontChain) draw(dst *image.RGBA, s string, x, baseline int, col color.Color) {
	if s == "" {
		return
	}
	dot := fixed.P(x, baseline)
	runStart := dot
	runIndex := -1
	var run strings.Builder
	flush := func() {
		if run.Len() == 0 {
			return
		}
		d := font.Drawer{
			Dst:  dst,
			Src:  image.NewUniform(col),
			Face: noKernFace{c.faces[runIndex]},
			Dot:  runStart,
		}
		d.DrawString(run.String())
		run.Reset()
	}
	for _, r := range s {
		m := c.rune(r)
		if m.skip {
			// Formatting/combining rune: no advance, no standalone glyph.
			continue
		}
		if m.index != runIndex {
			flush()
			runIndex = m.index
			runStart = dot
		}
		run.WriteRune(r)
		dot.X += m.advance
	}
	flush()
}
