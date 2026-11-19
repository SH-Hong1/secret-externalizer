package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"time"
)

//go:embed static
var staticFS embed.FS

// staticFiles returns the embedded static files, rooted at the static
// directory.
func staticFiles() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(fmt.Sprintf("opening embedded static files: %v", err))
	}
	return sub
}

// staticTypes maps the extensions of the files the server serves to their
// content types. A file with another extension is not served.
func staticTypes() map[string]string {
	return map[string]string{
		".css":   "text/css; charset=utf-8",
		".js":    "text/javascript; charset=utf-8",
		".woff2": "font/woff2",
		".txt":   "text/plain; charset=utf-8",
	}
}

// staticFile is one file the server serves under /static/.
type staticFile struct {
	data        []byte
	contentType string
	// version is the first 8 hex digits of the SHA-256 of data.
	version string
}

// assets holds the static files in memory, by their path under static/.
type assets struct {
	files map[string]staticFile
}

// loadAssets reads every file in fsys whose extension staticTypes knows. It
// panics when fsys cannot be read, because fsys is embedded in the binary
// and a failure means the binary itself is broken.
func loadAssets(fsys fs.FS) *assets {
	types := staticTypes()
	a := &assets{files: make(map[string]staticFile)}
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		ct, ok := types[path.Ext(name)]
		if d.IsDir() || !ok {
			return nil
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("reading %s: %w", name, err)
		}
		sum := sha256.Sum256(data)
		a.files[name] = staticFile{data: data, contentType: ct, version: hex.EncodeToString(sum[:4])}
		return nil
	})
	if err != nil {
		panic(fmt.Sprintf("loading static files: %v", err))
	}
	return a
}

// url returns the address of the static file name with its version in the
// query, so that a changed file gets a new address and browsers may keep
// each version for good. It fails for a name that is not a static file,
// which makes a template that names one fail when it runs.
func (a *assets) url(name string) (string, error) {
	f, ok := a.files[name]
	if !ok {
		return "", fmt.Errorf("unknown static file %q", name)
	}
	return "/static/" + name + "?v=" + f.version, nil
}

// handleStatic serves the static files. Only the files loadAssets loaded are
// served, looked up by their exact path, so a path with "..", a directory,
// or an unknown name gets the 404 page. The files never change while the
// program runs and the addresses that url builds carry their version, so
// browsers may cache them for a year; this replaces the no-store that
// securityHeaders sets.
func handleStatic(logger *slog.Logger, rd *renderer, a *assets) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := a.files[r.PathValue("path")]
		if !ok {
			rd.fail(w, r, logger, http.StatusNotFound, errors.New("no static file"))
			return
		}
		h := w.Header()
		h.Set("Content-Type", f.contentType)
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
		h.Set("ETag", `"`+f.version+`"`)
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.data))
	})
}
