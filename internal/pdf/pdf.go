// Package pdf turns an HTML document into a PDF by sending it, with the
// files it names, to a Gotenberg server.
//
// One conversion is one multipart request to the Chromium HTML route. The
// page is sent as index.html and the footer as footer.html; every other file
// is sent beside them and named from the page by its bare name. An error
// names the HTTP status at most and never holds the converter's response
// body.
package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	// ErrUnavailable means the converter could not be reached or did not
	// answer in time.
	ErrUnavailable = errors.New("pdf converter is unavailable")
	// ErrRejected means the converter answered with something other than
	// a PDF of at most MaxBytes.
	ErrRejected = errors.New("pdf converter rejected the document")
)

// MaxBytes is the largest PDF Convert returns.
const MaxBytes = 64 << 20

// route is the Gotenberg route that prints an HTML page with Chromium.
const route = "/forms/chromium/convert/html"

// Names Gotenberg gives a meaning: the page, and the documents Chromium
// draws in the top and bottom margin of every sheet.
const (
	indexName  = "index.html"
	headerName = "header.html"
	footerName = "footer.html"
)

// File is a file the document names by Name, which has no folder part.
type File struct {
	Name string
	Data []byte
}

// Document is what one conversion sends: the page, the footer of every
// sheet, and the files they name.
type Document struct {
	Index  []byte
	Footer []byte
	Files  []File
}

// Client converts documents on one Gotenberg server.
type Client struct {
	endpoint string
	http     *http.Client
	maxBytes int64
}

// New returns a client for the server at baseURL, an absolute http or
// https address without a path, query or user info. It contacts nothing.
func New(baseURL string, client *http.Client) (*Client, error) {
	if client == nil {
		return nil, errors.New("pdf: no http client")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		// The parse error repeats the address, which may hold user info.
		return nil, errors.New("pdf: the converter address is not a URL")
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, errors.New("pdf: the converter address must start with http:// or https://")
	case u.User != nil:
		return nil, errors.New("pdf: the converter address must not hold user info")
	case u.Host == "" || u.Opaque != "":
		return nil, errors.New("pdf: the converter address has no host")
	case u.Path != "" && u.Path != "/", u.RawQuery != "", u.ForceQuery, u.Fragment != "":
		return nil, fmt.Errorf("pdf: the converter address %q must not have a path, query or fragment", baseURL)
	}
	return &Client{
		endpoint: u.Scheme + "://" + u.Host + route,
		http:     client,
		maxBytes: MaxBytes,
	}, nil
}

// Convert returns the PDF of doc on A4 sheets. It fails with an error
// matching ErrUnavailable or ErrRejected, and without sending anything
// when a file name is empty, repeats, is index.html, header.html or footer.html, or
// holds a slash, a backslash or a control character.
func (c *Client) Convert(ctx context.Context, doc Document) ([]byte, error) {
	if err := checkNames(doc.Files); err != nil {
		return nil, err
	}
	body, contentType, err := encode(doc)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("pdf: building the request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := c.http.Do(req)
	if err != nil {
		// No answer: the converter is down or slow, or ctx was cancelled
		// or ran out, which the caller sees as the converter not answering
		// in time. The context error stays matchable through the chain.
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() {
		_ = resp.Body.Close() // a read-only response body; nothing was written that a close could lose
	}()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		// Gotenberg answers 503 when its own time limit ends or Chromium is
		// restarting.
		return nil, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	default:
		return nil, fmt.Errorf("%w: status %d", ErrRejected, resp.StatusCode)
	}

	out, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: reading the PDF: %w", ErrUnavailable, err)
	}
	if int64(len(out)) > c.maxBytes {
		return nil, fmt.Errorf("%w: the PDF is larger than %d bytes", ErrRejected, c.maxBytes)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		return nil, fmt.Errorf("%w: the answer is not a PDF", ErrRejected)
	}
	return out, nil
}

// checkNames returns an error matching ErrRejected for the first file name
// that Gotenberg would not keep as given or that would replace the page,
// header or footer.
func checkNames(files []File) error {
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		if err := checkName(f.Name); err != nil {
			return fmt.Errorf("%w: file name %q %w", ErrRejected, f.Name, err)
		}
		if seen[f.Name] {
			return fmt.Errorf("%w: file name %q is used twice", ErrRejected, f.Name)
		}
		seen[f.Name] = true
	}
	return nil
}

var (
	errNameEmpty    = errors.New("is empty")
	errNameReserved = errors.New("is kept for the page, header or footer")
	errNameFolder   = errors.New("has a folder part")
	errNameControl  = errors.New("holds a control character or is not UTF-8")
)

// checkName compares with the reserved names case-sensitively, because the
// converter writes the files to a case-sensitive Linux file system where
// Footer.html does not replace footer.html.
func checkName(name string) error {
	switch {
	case name == "":
		return errNameEmpty
	case name == indexName || name == headerName || name == footerName:
		return errNameReserved
	case name == "." || name == ".." || strings.ContainsAny(name, `/\`):
		return errNameFolder
	case !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl):
		return errNameControl
	}
	return nil
}

// encode writes the multipart body of one conversion.
func encode(doc Document) (io.Reader, string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	write := func(name string, data []byte) error {
		w, err := mw.CreateFormFile("files", name)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	if err := write(indexName, doc.Index); err != nil {
		return nil, "", fmt.Errorf("pdf: encoding the request: %w", err)
	}
	if len(doc.Footer) > 0 {
		if err := write(footerName, doc.Footer); err != nil {
			return nil, "", fmt.Errorf("pdf: encoding the request: %w", err)
		}
	}
	for _, f := range doc.Files {
		if err := write(f.Name, f.Data); err != nil {
			return nil, "", fmt.Errorf("pdf: encoding the request: %w", err)
		}
	}
	// A4 sheets with the margins the footer needs, backgrounds printed, and
	// the sheet size taken from these fields rather than from a CSS @page
	// rule. A length without a unit would also be read as inches.
	fields := [][2]string{
		{"paperSize", "A4"},
		{"marginTop", "0.6in"},
		{"marginBottom", "0.8in"},
		{"marginLeft", "0.6in"},
		{"marginRight", "0.6in"},
		{"printBackground", "true"},
		{"preferCssPageSize", "false"},
	}
	for _, field := range fields {
		if err := mw.WriteField(field[0], field[1]); err != nil {
			return nil, "", fmt.Errorf("pdf: encoding the request: %w", err)
		}
	}
	if err := mw.Close(); err != nil {
		return nil, "", fmt.Errorf("pdf: encoding the request: %w", err)
	}
	return &buf, mw.FormDataContentType(), nil
}
