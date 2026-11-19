package scan

import (
	"context"
	"fmt"

	"github.com/SH-Hong1/secret-externalizer/internal/textfile"
)

// counter is a Scanner that also reports how many results it dropped.
type counter interface {
	ScanWithCounts(ctx context.Context, dir string) ([]Raw, Counts, error)
}

// wholeFile reports whether r covers a whole file, as a key file result
// does.
func (r Raw) wholeFile() bool { return r.Offset == 0 && r.EndOffset == -1 }

// detect runs every scanner over the project folder dir and, when there are
// EUC-KR files, over the folder of copies. It returns the results that refer
// to files the scan can place them in, the version of each scanner, and how
// many results the scanners themselves dropped. A scanner that fails is a
// *toolError.
func (r *Runner) detect(ctx context.Context, dir, copies string, files fileList) ([]Raw, map[string]string, int, error) {
	versions := make(map[string]string, len(r.scanners))
	var kept []Raw
	dropped := 0
	for _, s := range r.scanners {
		v, err := s.Version(ctx)
		if err != nil {
			return nil, nil, 0, &toolError{tool: s.Name(), err: fmt.Errorf("reading version: %w", err)}
		}
		versions[s.Name()] = v

		folders := []string{dir}
		if files.copied > 0 {
			folders = append(folders, copies)
		}
		for i, folder := range folders {
			raws, counts, err := scanFolder(ctx, s, folder)
			if err != nil {
				return nil, nil, 0, &toolError{tool: s.Name(), err: err}
			}
			kept = append(kept, files.keep(raws, i == 1)...)
			dropped += counts.Dropped
		}
	}
	return kept, versions, dropped, nil
}

// scanFolder runs s over dir, with counts when s reports them.
func scanFolder(ctx context.Context, s Scanner, dir string) ([]Raw, Counts, error) {
	if c, ok := s.(counter); ok {
		return c.ScanWithCounts(ctx, dir)
	}
	raws, err := s.Scan(ctx, dir)
	return raws, Counts{}, err
}

// keep returns the results that the scan uses. fromCopy tells whether the
// results come from the folder of copies. Results it leaves out are not
// counted as dropped: they are about files the scan does not report on, or
// they repeat a result from the other folder.
//
// A result for a file the scan did not list (excluded, a symbolic link,
// outside the folder) is left out. A whole-file result needs only a listed
// file; it is taken from the project folder, where the file has its original
// bytes. Any other result needs a file read as text: for an EUC-KR file it
// is taken from its UTF-8 copy, and the project folder's result, at EUC-KR
// offsets, is left out.
func (l fileList) keep(raws []Raw, fromCopy bool) []Raw {
	var kept []Raw
	for _, raw := range raws {
		f, listed := l.files[raw.File]
		switch {
		case !listed:
		case raw.wholeFile():
			if !fromCopy {
				kept = append(kept, raw)
			}
		case f.text && (f.encoding == textfile.EUCKR) == fromCopy:
			kept = append(kept, raw)
		}
	}
	return kept
}
