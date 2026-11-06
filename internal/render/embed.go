package render

import _ "embed"

// The renderer embeds a fallback chain of OFL-licensed Noto faces; see
// fonts/README.md for provenance and SHA-256 digests. The order is significant:
// NotoSansCJKsc covers CJK, Latin and many symbols, NotoEmoji adds outline
// (monochrome) emoji glyphs, NotoSansMath adds the Mathematical Alphanumeric
// Symbols block (decorative nicknames such as "𝐶𝑟𝑎𝑛𝑦𝑜𝑧𝑒𝑛"), and
// NotoSansSymbols2 fills in the remaining monochrome symbol/dingbat ranges.
// NotoEmoji is an outline font (glyf/gvar), not CBDT/COLR, so x/image can
// rasterise it; it is monochrome, and ZWJ sequences are not shaped.
//
//go:embed fonts/NotoSansCJKsc-Regular.otf
var fontFile []byte

//go:embed fonts/NotoEmoji-Variable.ttf
var emojiFontFile []byte

//go:embed fonts/NotoSansMath-Regular.ttf
var mathFontFile []byte

//go:embed fonts/NotoSansSymbols2-Regular.ttf
var symbolsFontFile []byte

// The OFL licence texts travel with the fonts, so a binary distributed on its
// own still carries the attribution its font licences require. Docker images
// copy the same files to /licenses/fonts/ as well.
//
//go:embed fonts/OFL.txt
var cjkLicenseFile []byte

//go:embed fonts/NotoEmoji-OFL.txt
var emojiLicenseFile []byte

//go:embed fonts/NotoSansMath-OFL.txt
var mathLicenseFile []byte

//go:embed fonts/NotoSansSymbols2-OFL.txt
var symbolsLicenseFile []byte
