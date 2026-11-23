package web

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/SH-Hong1/secret-externalizer/internal/pdf"
)

// cssURL matches a url() in a stylesheet and captures its address without
// the quotes.
var cssURL = regexp.MustCompile(`url\(\s*["']?([^"')\s]*)["']?\s*\)`)

// fontFolder is the folder under static/ the stylesheet names fonts in.
const fontFolder = "fonts/"

// printFiles returns the files a PDF document names: app.css with every
// font address turned into the bare file name, and the fonts it names. NewServer
// calls it once, before it serves anything, and it panics when the
// stylesheet names a font a does not hold.
//
// The converter lets Chromium open only the files directly inside the folder
// of one request, so a document may name a file only by its bare name. A
// url() that names anything outside the font folder also panics, because the
// PDF could not draw it.
func printFiles(a *assets) []pdf.File {
	css, ok := a.files["app.css"]
	if !ok {
		panic("printing: no static file app.css")
	}
	var (
		fonts   []pdf.File
		seen    = make(map[string]bool)
		problem error
	)
	out := cssURL.ReplaceAllFunc(css.data, func(m []byte) []byte {
		addr := string(cssURL.FindSubmatch(m)[1])
		name, inFolder := strings.CutPrefix(addr, fontFolder)
		font, held := a.files[addr]
		switch {
		case !inFolder || name == "" || strings.ContainsAny(name, `/\`):
			problem = fmt.Errorf("app.css names %q, which is not a file in static/%s", addr, fontFolder)
			return m
		case !held:
			problem = fmt.Errorf("app.css names the font %q, which static/ does not hold", addr)
			return m
		}
		if !seen[name] {
			seen[name] = true
			fonts = append(fonts, pdf.File{Name: name, Data: font.data})
		}
		return []byte("url(" + name + ")")
	})
	if problem != nil {
		panic("printing: " + problem.Error())
	}
	return append([]pdf.File{{Name: "app.css", Data: out}}, fonts...)
}
