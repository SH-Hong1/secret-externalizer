package web

import (
	"slices"
	"strings"
	"testing"
)

func TestPrintFiles(t *testing.T) {
	a := loadAssets(staticFiles())
	files := printFiles(a)

	var names []string
	for _, f := range files {
		names = append(names, f.Name)
	}
	want := []string{
		"app.css",
		"IBMPlexSansKR-1.1.0-Regular.woff2", "IBMPlexSansKR-1.1.0-SemiBold.woff2", "IBMPlexSansKR-1.1.0-Bold.woff2",
		"D2Coding-1.4.0-Regular.woff2", "D2Coding-1.4.0-Bold.woff2",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("printFiles() names = %v, want %v", names, want)
	}
	css := string(files[0].Data)
	if strings.Contains(css, "fonts/") {
		t.Errorf("printFiles() app.css still names a font by its folder")
	}
	if !strings.Contains(css, "url(IBMPlexSansKR-1.1.0-Regular.woff2)") {
		t.Errorf("printFiles() app.css lacks url(IBMPlexSansKR-1.1.0-Regular.woff2)")
	}
	for _, f := range files[1:] {
		if !slices.Equal(f.Data, a.files["fonts/"+f.Name].data) {
			t.Errorf("printFiles() %s differs from static/fonts/%s", f.Name, f.Name)
		}
	}
	if strings.Contains(string(a.files["app.css"].data), "url(IBMPlex") {
		t.Errorf("printFiles() changed the served app.css")
	}
}

func TestPrintFiles_Panics(t *testing.T) {
	tests := []struct {
		name string
		css  string
	}{
		{"missing font", `@font-face{src:url(fonts/Absent.woff2) format("woff2")}`},
		{"quoted missing font", `@font-face{src:url("fonts/Absent.woff2")}`},
		{"address outside fonts", `body{background:url(img/a.png)}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &assets{files: map[string]staticFile{"app.css": {data: []byte(tt.css)}}}
			defer func() {
				if recover() == nil {
					t.Errorf("printFiles() with app.css %q did not panic", tt.css)
				}
			}()
			printFiles(a)
		})
	}
}
