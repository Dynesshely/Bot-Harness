# Embedded fonts (fallback chain)

The table-to-PNG renderer embeds **four** OFL-licensed Noto faces with
`go:embed` (see `internal/render/embed.go`) and resolves every rune against
them in this fixed order:

| # | Family | File | Role |
|---|---|---|---|
| 0 | Noto Sans CJK SC Regular | `NotoSansCJKsc-Regular.otf` | Primary face: CJK, Latin, punctuation, many symbols. Also defines ascent/line height for every run. |
| 1 | Noto Emoji (variable, default Regular instance) | `NotoEmoji-Variable.ttf` | Monochrome **outline** emoji (U+1F300…, U+2600…, U+2764, flags, …). |
| 2 | Noto Sans Math Regular | `NotoSansMath-Regular.ttf` | Mathematical Alphanumeric Symbols (stylised nicknames), math operators. |
| 3 | Noto Sans Symbols2 Regular | `NotoSansSymbols2-Regular.ttf` | Remaining monochrome symbol/dingbat ranges (⌫ ⌥ ⏻ …). |

The first face whose `GlyphIndex(rune) != 0` wins; `0` is `.notdef`, i.e. "this
face cannot draw the rune". Emoji is placed **before** Math/Symbols2 on
purpose: NotoEmoji has the recognisable emoji designs for the presentation
ranges it shares with Symbols2 (⬛ ☑ ⛔ …), while CJK still wins everything it
already covers (including ★ → ① and ordinary ASCII). The decision lives in
`fontChain.rune` / `fontChain.selectedFontIndex` (`internal/render/fonts.go`)
and is cached per rune.

## Provenance and digests

| Field | Noto Sans CJK SC | Noto Emoji | Noto Sans Math | Noto Sans Symbols2 |
|---|---|---|---|---|
| Upstream | https://github.com/notofonts/noto-cjk | https://github.com/google/fonts/tree/main/ofl/notoemoji | https://github.com/google/fonts/tree/main/ofl/notosansmath | https://github.com/google/fonts/tree/main/ofl/notosanssymbols2 |
| Font URL | https://raw.githubusercontent.com/notofonts/noto-cjk/main/Sans/OTF/SimplifiedChinese/NotoSansCJKsc-Regular.otf | https://raw.githubusercontent.com/google/fonts/main/ofl/notoemoji/NotoEmoji%5Bwght%5D.ttf | https://raw.githubusercontent.com/google/fonts/main/ofl/notosansmath/NotoSansMath-Regular.ttf | https://raw.githubusercontent.com/google/fonts/main/ofl/notosanssymbols2/NotoSansSymbols2-Regular.ttf |
| License URL | https://raw.githubusercontent.com/notofonts/noto-cjk/main/Sans/LICENSE | https://raw.githubusercontent.com/google/fonts/main/ofl/notoemoji/OFL.txt | https://raw.githubusercontent.com/google/fonts/main/ofl/notosansmath/OFL.txt | https://raw.githubusercontent.com/google/fonts/main/ofl/notosanssymbols2/OFL.txt |
| License | SIL OFL 1.1 (`OFL.txt`) | SIL OFL 1.1 (`NotoEmoji-OFL.txt`) | SIL OFL 1.1 (`NotoSansMath-OFL.txt`) | SIL OFL 1.1 (`NotoSansSymbols2-OFL.txt`) |
| Size | 16,437,364 B | 1,982,596 B | 1,015,396 B | 1,233,128 B |
| SHA-256 | `2c76254f6fc379fddfce0a7e84fb5385bb135d3e399294f6eeb6680d0365b74b` | `de6c18832938afc99caf132b39d6a30a19bac7f2e812e28db2535b4608d27551` | `3f495fe933c06786e4d5f6d86b8ee70b6753a68ee3b9d87528726de0f6e2c47d` | `7d5fb73b7ca67a6798101741f5d280a3d016a56a197afcd4199dbb57b4b82a21` |
| OFL.txt SHA-256 | `6a73f9541c2de74158c0e7cf6b0a58ef774f5a780bf191f2d7ec9cc53efe2bf2` | `500bb1ccf43df7bbb522112f9133a52b16e1c35e809632f5d8609b179152de5b` | `403a95275b469061b7d4371c328e0ada3bc7d63328abe2e88aad5cd243b2fe21` | `b118dd41337806a5d4797052c77caf3bd096aed783e5eb21b4d11154351e1ac0` |

The CJK face is stored under the historical name `OFL.txt` so that existing
links keep working; the Google Fonts faces keep their upstream OFL text in
separate per-family files.

`NotoEmoji-Variable.ttf` is the upstream `NotoEmoji[wght].ttf` variable font.
Its default instance is Regular, and the renderer does **not** vary the weight:
it parses/instances the default. It is an outline font — the only relevant
sfnt tables are `glyf` / `loca` / `gvar` / `fvar`; there is **no** `CBDT`,
`CBLC`, `COLR`, `CPAL` or `SVG ` table. `golang.org/x/image/font/opentype`
parses it successfully, so it needs no static-instance conversion.

## Coverage

| Range | Face |
|---|---|
| U+0000–U+04FF (Latin, Greek, Cyrillic) | CJK |
| CJK Unified Ideographs, kana, hangul, fullwidth forms | CJK |
| U+2460–U+24FF (circled digits/letters, ①), U+25xx–U+27xx basics (★ → ✓) | CJK |
| Emoji blocks/presentation ranges not already covered by CJK (U+1F300–U+1FAFF, U+1F1E6–U+1F1FF regional indicators, U+2764, U+2600-range emoji such as ☺ ☹ ✌, ⌚, 🀄 …) | Emoji |
| U+1D400–U+1D7FF (Mathematical Alphanumeric Symbols: `𝐶𝑟𝑎𝑛𝑦𝑜𝑧𝑒𝑛` U+1D436…, `𝓒𝓻𝓪𝓷𝔂𝓸𝔃𝓮𝓷` U+1D4D2…) | Math |
| Math operators/arrows missing from CJK (∑ ∮ ⇫…) | Math |
| Remaining symbols not covered above (⌫ ⌥ ⏻ ␀ …) | Symbols2 |

CJK comes first, so a rune both CJK and NotoEmoji cover (ordinary ASCII
digits/punctuation, ☀ ☁ ☂ ☃, 🈚 🈯 …) still uses CJK. Everything outside all
four faces has no glyph.

## Limits

- **Monochrome emoji only.** NotoEmoji is an outline face; x/image rasterises
  outlines with a single ink colour (the table's body colour). It has no
  CBDT/CBLC/COLR/CPAL/SVG tables and x/image has no colour-glyph rasteriser, so
  emoji appear as monochrome line/solid glyphs, not the colour emoji users see
  in QQ. This is the accepted trade-off: a colour font would render as tofu.
- **No shaping or kerning.** Text is measured and drawn rune by rune; the
  drawer's kerning is disabled so measurement and drawing agree exactly. There
  are no ligatures, no GSUB and no bidi. Consequently **ZWJ sequences are not
  composed into one glyph**: 👨‍👩‍👧 draws three component emoji side by side,
  🏳️‍🌈 draws a white flag plus a rainbow, and 🇨🇳 draws two regional-indicator
  letters. They do not crash and stay readable, which is the deliberate
  fallback for a monochrome outline face.
- **Zero-width / combining runes are skipped.** VS15/VS16 (U+FE0E/U+FE0F),
  ZWJ/ZWNJ/BOM, bidi controls, tag characters (subdivision flags), emoji
  skin-tone modifiers (U+1F3FB–U+1F3FF) and combining enclosing keycap
  (U+20E3) neither advance the pen nor draw a glyph. That makes `❤️`
  pixel-identical to `❤` instead of a tofu box, and avoids stray modifier
  glyphs; the flip side is that keycaps render only the base character.
- **Runes nothing covers still render as tofu.** The chain falls back to the
  primary face's `.notdef` box, which is the pre-fallback behaviour.
- **Vertical metrics come from the CJK face only.** Emoji/Math/Symbols2 draw
  at the same pixel size on the same baseline; their own ascent/descent are
  ignored so row heights stay uniform.

## Why these fonts

`𝐶𝑟𝑎𝑛𝑦𝑜𝑧𝑒𝑛`-style nicknames live in the Mathematical Alphanumeric Symbols
block (U+1D400–U+1D7FF), which Noto Sans CJK SC does not cover — the renderer
used to emit one `.notdef` box per rune. Noto Sans Math is the OFL font Google
ships for exactly that block; Noto Sans Symbols2 fills the monochrome symbol
ranges that neither of the other three covers. Noto Emoji covers the emoji
code points with outline glyphs that x/image can actually draw. Cost:
**+4,231,120 B** of embedded secondary font data (NotoEmoji 1,982,596 B +
Math 1,015,396 B + Symbols2 1,233,128 B) on top of the ~16.4 MB CJK face.
