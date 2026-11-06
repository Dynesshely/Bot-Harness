package render

import (
	"bytes"
	"testing"
)

// The four font licences must be compiled into the binary, not merely left in
// the source tree: a single-binary distribution has to carry its OFL texts.
func TestFontLicensesAreEmbedded(t *testing.T) {
	licenses := map[string][]byte{
		"NotoSansCJKsc":    cjkLicenseFile,
		"NotoEmoji":        emojiLicenseFile,
		"NotoSansMath":     mathLicenseFile,
		"NotoSansSymbols2": symbolsLicenseFile,
	}
	for name, text := range licenses {
		if len(text) == 0 {
			t.Fatalf("%s licence is empty", name)
		}
		if !bytes.Contains(bytes.ToUpper(text), []byte("SIL OPEN FONT LICENSE")) {
			t.Fatalf("%s licence does not look like an OFL text", name)
		}
	}
}
