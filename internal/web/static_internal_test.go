package web

import (
	"bytes"
	"html/template"
	"regexp"
	"testing"
	"testing/fstest"
)

func TestAsset(t *testing.T) {
	a := loadAssets(staticFiles())
	got, err := a.url("app.css")
	if err != nil {
		t.Fatalf(`url("app.css") error = %v`, err)
	}
	if !regexp.MustCompile(`^/static/app\.css\?v=[0-9a-f]{8}$`).MatchString(got) {
		t.Errorf(`url("app.css") = %q, want /static/app.css?v= and 8 hex digits`, got)
	}
	got, err = a.url("fonts/D2Coding-1.4.0-Regular.woff2")
	if err != nil || !regexp.MustCompile(`^/static/fonts/D2Coding-1\.4\.0-Regular\.woff2\?v=[0-9a-f]{8}$`).MatchString(got) {
		t.Errorf(`url("fonts/D2Coding-1.4.0-Regular.woff2") = %q, %v, want a versioned font URL`, got, err)
	}
	if got, err := a.url("nope.css"); err == nil {
		t.Errorf(`url("nope.css") = %q, nil, want an error`, got)
	}
}

func TestAsset_VersionFollowsContent(t *testing.T) {
	one := loadAssets(fstest.MapFS{"app.css": {Data: []byte("a{}")}})
	two := loadAssets(fstest.MapFS{"app.css": {Data: []byte("b{}")}})
	u1, err1 := one.url("app.css")
	u2, err2 := two.url("app.css")
	if err1 != nil || err2 != nil || u1 == u2 {
		t.Errorf(`url("app.css") for two contents = %q (%v), %q (%v), want two different URLs`, u1, err1, u2, err2)
	}
}

func TestAsset_UnknownNameFailsTemplate(t *testing.T) {
	a := loadAssets(staticFiles())
	tmpl := template.Must(template.New("t").Funcs(template.FuncMap{"asset": a.url}).Parse(`<link href="{{asset "nope.css"}}">`))
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, nil); err == nil {
		t.Errorf(`executing {{asset "nope.css"}} = %q, nil, want an error`, buf.String())
	}
}
