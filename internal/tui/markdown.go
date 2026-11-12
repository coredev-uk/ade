package tui

import (
	"github.com/coredev-uk/ade/internal/mdrender"
)

// mdLines renders markdown (issue and MR descriptions, AI summaries) to lines
// of at most w cells through glamour in the Interchange style (internal/
// mdrender): weight for emphasis, faint quotes and addresses, no background.
// NO_COLOR and the ascii glyph set are honoured.
func (m model) mdLines(src string, w int) []string {
	return mdrender.Render(src, mdrender.FromEnv(max(w, 10), m.g == asciiGlyphs))
}

// mdPreview is mdLines cut to at most rows lines; a cut ends in a faint "…".
func (m model) mdPreview(src string, w, rows int) []string {
	l := m.mdLines(src, w)
	if len(l) <= rows {
		return l
	}
	return append(l[:rows-1:rows-1], stFaint.Render(m.g.Ell))
}
