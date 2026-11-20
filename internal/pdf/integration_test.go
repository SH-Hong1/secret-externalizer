package pdf_test

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/pdf"
)

// fontFile is the Korean body font the web pages use. testdata holds a link
// to the copy under internal/web/static/fonts.
const fontFile = "IBMPlexSansKR-1.1.0-Regular.woff2"

// realClient returns a client for the Gotenberg server at TEST_PDF_URL, the
// compose pdf-test service, or skips the test when it is not set.
func realClient(t *testing.T) *pdf.Client {
	t.Helper()
	baseURL := os.Getenv("TEST_PDF_URL")
	if baseURL == "" {
		t.Skip("TEST_PDF_URL is not set; start the compose pdf-test service with --profile test and set TEST_PDF_URL=http://127.0.0.1:13000")
	}
	c, err := pdf.New(baseURL, &http.Client{Timeout: 25 * time.Second})
	if err != nil {
		t.Fatalf("New(%q) error = %v", baseURL, err)
	}
	return c
}

// convert runs Convert with the same 20 second limit that web uses.
func convert(t *testing.T, c *pdf.Client, doc pdf.Document) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	out, err := c.Convert(ctx, doc)
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Fatalf("Convert() = %q..., want a PDF", out[:min(len(out), 16)])
	}
	return out
}

func TestConvert_Real(t *testing.T) {
	c := realClient(t)
	font, err := os.ReadFile("testdata/" + fontFile)
	if err != nil {
		t.Fatalf("reading the font: %v", err)
	}

	t.Run("Korean text in the sent font", func(t *testing.T) {
		out := convert(t, c, pdf.Document{
			Index: []byte(`<!doctype html><html lang="ko"><head><meta charset="utf-8"><link rel="stylesheet" href="app.css"></head>` +
				`<body><h1>시험 리포트</h1><p>가나다라마바사 한글 문단입니다.</p></body></html>`),
			Footer: []byte(`<html><head><style>body{font-size:8px;margin:0 0.6in}</style></head>` +
				`<body>secret-externalizer | scan #1 | <span class="pageNumber"></span> / <span class="totalPages"></span></body></html>`),
			Files: []pdf.File{
				{Name: "app.css", Data: []byte(`@font-face{font-family:"IBM Plex Sans KR";src:url("` + fontFile + `") format("woff2");font-weight:400}` +
					`body{font-family:"IBM Plex Sans KR"}`)},
				{Name: fontFile, Data: font},
			},
		})
		// The image falls back to Noto Sans CJK KR when the sent font is not
		// used, so the embedded font name tells which one drew the text. The
		// font descriptors are not compressed in the PDF Chromium writes.
		if !bytes.Contains(out, []byte("+IBMPlexSansKR")) {
			t.Errorf("Convert() of a Korean page with %s gave a PDF without that font embedded", fontFile)
		}
	})

	t.Run("script and outside image", func(t *testing.T) {
		convert(t, c, pdf.Document{
			Index: []byte(`<!doctype html><html><body><p id="p">static</p>` +
				`<script>document.getElementById("p").textContent = "script ran";</script>` +
				`<img src="http://db.invalid/x.png"><img src="https://example.com/x.png"></body></html>`),
		})
	})
}
