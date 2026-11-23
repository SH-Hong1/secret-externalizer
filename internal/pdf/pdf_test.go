package pdf_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/pdf"
)

// bodyMarker is what the fake converter puts in every error body. No error
// Convert returns may contain it.
const bodyMarker = "dev-only-fake-converter-body"

func newClient(t *testing.T, baseURL string) *pdf.Client {
	t.Helper()
	c, err := pdf.New(baseURL, &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New(%q) error = %v", baseURL, err)
	}
	return c
}

func testDocument() pdf.Document {
	return pdf.Document{
		Index:  []byte("<!doctype html><p>본문</p>"),
		Footer: []byte("<html><body><span class=\"pageNumber\"></span></body></html>"),
		Files: []pdf.File{
			{Name: "app.css", Data: []byte("body{color:black}")},
			{Name: "IBMPlexSansKR-1.1.0-Regular.woff2", Data: []byte("dev-only-fake-font")},
		},
	}
}

// received is what the fake converter read from one request.
type received struct {
	Method string
	Path   string
	Files  map[string]string
	Fields map[string]string
}

func readRequest(t *testing.T, r *http.Request) received {
	t.Helper()
	got := received{Method: r.Method, Path: r.URL.Path, Files: map[string]string{}, Fields: map[string]string{}}
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		t.Errorf("Content-Type = %q, want multipart/form-data", r.Header.Get("Content-Type"))
		return got
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return got
		}
		if err != nil {
			t.Errorf("reading part: %v", err)
			return got
		}
		data, err := io.ReadAll(part)
		if err != nil {
			t.Errorf("reading part %q: %v", part.FormName(), err)
			return got
		}
		switch {
		case part.FormName() == "files":
			if _, dup := got.Files[part.FileName()]; dup {
				t.Errorf("file %q sent twice", part.FileName())
			}
			got.Files[part.FileName()] = string(data)
		case part.FileName() != "":
			t.Errorf("file %q sent in field %q, want files", part.FileName(), part.FormName())
		default:
			got.Fields[part.FormName()] = string(data)
		}
	}
}

func TestConvert_SendsDocument(t *testing.T) {
	var got received
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = readRequest(t, r)
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = io.WriteString(w, "%PDF-1.7 fake") // the test reads the result below
	}))
	t.Cleanup(srv.Close)

	doc := testDocument()
	out, err := newClient(t, srv.URL).Convert(t.Context(), doc)
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	if string(out) != "%PDF-1.7 fake" {
		t.Errorf("Convert() = %q, want %q", out, "%PDF-1.7 fake")
	}

	want := received{
		Method: http.MethodPost,
		Path:   "/forms/chromium/convert/html",
		Files: map[string]string{
			"index.html":                        string(doc.Index),
			"footer.html":                       string(doc.Footer),
			"app.css":                           "body{color:black}",
			"IBMPlexSansKR-1.1.0-Regular.woff2": "dev-only-fake-font",
		},
		Fields: map[string]string{
			"paperSize":         "A4",
			"marginTop":         "0.6in",
			"marginBottom":      "0.8in",
			"marginLeft":        "0.6in",
			"marginRight":       "0.6in",
			"printBackground":   "true",
			"preferCssPageSize": "false",
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Convert() sent a request that differs (-want +got):\n%s", diff)
	}
}

func TestConvert_NoFooter(t *testing.T) {
	var got received
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = readRequest(t, r)
		_, _ = io.WriteString(w, "%PDF-1.7 fake") // the test reads the result below
	}))
	t.Cleanup(srv.Close)

	doc := testDocument()
	doc.Footer = nil
	if _, err := newClient(t, srv.URL).Convert(t.Context(), doc); err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	if _, ok := got.Files["footer.html"]; ok {
		t.Errorf("Convert() of a document without a footer sent footer.html")
	}
}

func TestConvert_Errors(t *testing.T) {
	answer := func(status int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body) // the client under test reads it, or not
		}
	}
	tests := []struct {
		name     string
		handler  http.HandlerFunc // nil: nothing listens at the address
		maxBytes int64            // zero: the default
		timeout  time.Duration    // zero: no context deadline
		want     error
	}{
		{name: "closed port", want: pdf.ErrUnavailable},
		{name: "503", handler: answer(http.StatusServiceUnavailable, bodyMarker), want: pdf.ErrUnavailable},
		{name: "504", handler: answer(http.StatusGatewayTimeout, bodyMarker), want: pdf.ErrUnavailable},
		{name: "no answer before the deadline", timeout: 100 * time.Millisecond, want: pdf.ErrUnavailable, handler: func(_ http.ResponseWriter, r *http.Request) {
			// The server notices the client went away only once the body
			// is read.
			_, _ = io.Copy(io.Discard, r.Body) // the request is not looked at
			<-r.Context().Done()
		}},
		{name: "400", handler: answer(http.StatusBadRequest, bodyMarker), want: pdf.ErrRejected},
		{name: "413", handler: answer(http.StatusRequestEntityTooLarge, bodyMarker), want: pdf.ErrRejected},
		{name: "500", handler: answer(http.StatusInternalServerError, bodyMarker), want: pdf.ErrRejected},
		{name: "200 that is not a PDF", handler: answer(http.StatusOK, bodyMarker), want: pdf.ErrRejected},
		{name: "200 with an empty body", handler: answer(http.StatusOK, ""), want: pdf.ErrRejected},
		{name: "200 over the size limit", maxBytes: 16, handler: answer(http.StatusOK, "%PDF-1.7 "+strings.Repeat("x", 16)), want: pdf.ErrRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			if tt.handler == nil {
				srv.Close()
			} else {
				t.Cleanup(srv.Close)
			}
			c := newClient(t, srv.URL)
			if tt.maxBytes > 0 {
				pdf.SetMaxBytes(c, tt.maxBytes)
			}
			ctx := t.Context()
			if tt.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.timeout)
				defer cancel()
			}

			out, err := c.Convert(ctx, testDocument())
			if !errors.Is(err, tt.want) {
				t.Fatalf("Convert() = %q, %v, want error %v", out, err, tt.want)
			}
			if out != nil {
				t.Errorf("Convert() returned %d bytes with error %v, want none", len(out), err)
			}
			if strings.Contains(err.Error(), bodyMarker) {
				t.Errorf("Convert() error = %q, holds the converter's response body", err)
			}
		})
	}
}

func TestConvert_AcceptsMaxBytes(t *testing.T) {
	body := "%PDF-1.7 " + strings.Repeat("x", 7)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body) // the test reads the result below
	}))
	t.Cleanup(srv.Close)
	c := newClient(t, srv.URL)
	pdf.SetMaxBytes(c, int64(len(body)))

	out, err := c.Convert(t.Context(), testDocument())
	if err != nil || string(out) != body {
		t.Errorf("Convert() of a PDF of exactly the limit = %q, %v, want %q, nil", out, err, body)
	}
}

func TestConvert_RejectsNames(t *testing.T) {
	tests := []struct {
		name  string
		files []pdf.File
	}{
		{name: "empty", files: []pdf.File{{Name: ""}}},
		{name: "folder", files: []pdf.File{{Name: "a/b.css"}}},
		{name: "backslash", files: []pdf.File{{Name: `a\b.css`}}},
		{name: "dot", files: []pdf.File{{Name: "."}}},
		{name: "dot dot", files: []pdf.File{{Name: ".."}}},
		{name: "index.html", files: []pdf.File{{Name: "index.html"}}},
		{name: "header.html", files: []pdf.File{{Name: "header.html"}}},
		{name: "footer.html", files: []pdf.File{{Name: "footer.html"}}},
		{name: "repeated", files: []pdf.File{{Name: "app.css"}, {Name: "app.css"}}},
		{name: "newline", files: []pdf.File{{Name: "app\n.css"}}},
		{name: "delete character", files: []pdf.File{{Name: "app\x7f.css"}}},
		{name: "broken UTF-8", files: []pdf.File{{Name: "app\xff.css"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				_, _ = io.WriteString(w, "%PDF-1.7 fake") // the client under test reads it
			}))
			t.Cleanup(srv.Close)

			doc := testDocument()
			doc.Files = tt.files
			out, err := newClient(t, srv.URL).Convert(t.Context(), doc)
			if err == nil {
				t.Errorf("Convert() with files %q = %q, nil, want an error", tt.files, out)
			}
			if n := requests.Load(); n != 0 {
				t.Errorf("Convert() with files %q sent %d requests, want 0", tt.files, n)
			}
		})
	}
}

func TestNew_BadURL(t *testing.T) {
	for _, u := range []string{
		"",
		"pdf:3000",
		"ftp://pdf",
		"http://",
		"/forms",
		"http://pdf:3000/gotenberg",
		"http://pdf:3000?a=b",
		"http://pdf:3000#top",
		"http://user:dev-only-fake-password@pdf:3000",
	} {
		if c, err := pdf.New(u, &http.Client{}); err == nil {
			t.Errorf("New(%q) = %v, nil, want an error", u, c)
		}
	}
}

func TestNew_NilClient(t *testing.T) {
	if c, err := pdf.New("http://pdf:3000", nil); err == nil {
		t.Errorf("New with a nil http.Client = %v, nil, want an error", c)
	}
}

func TestNew_GoodURL(t *testing.T) {
	for _, u := range []string{"http://pdf:3000", "http://pdf:3000/", "https://127.0.0.1:13000"} {
		if _, err := pdf.New(u, &http.Client{}); err != nil {
			t.Errorf("New(%q) error = %v, want nil", u, err)
		}
	}
}

func TestNew_ErrorHoldsNoUserInfo(t *testing.T) {
	_, err := pdf.New("http://user:dev-only-fake-password@pdf:3000", &http.Client{})
	if err == nil || bytes.Contains([]byte(err.Error()), []byte("dev-only-fake-password")) {
		t.Errorf("New() error = %v, want an error without the password", err)
	}
}
