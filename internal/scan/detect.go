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

// drops is what the scanners told about the results they dropped
// themselves.
type drops struct {
	// count is the number of results dropped.
	count int
	// files holds the files of dropped results whose file is known.
	files map[string]bool
	// unknown is true when some dropped result has no known file.
	unknown bool
}

// add records the dropped results that c reports.
func (d *drops) add(c Counts) {
	d.count += c.Dropped
	if c.Dropped > len(c.DroppedFiles) {
		d.unknown = true
	}
	for _, f := range c.DroppedFiles {
		d.files[f] = true
	}
}

// detection is what running the scanners gave.
type detection struct {
	// raws are the results that refer to files the scan can place them in.
	raws []Raw
	// versions holds the version of each scanner, by name.
	versions map[string]string
	// dropped is what the scanners told about the results they dropped
	// themselves.
	dropped drops
	// errors holds, by scanner name, the number of errors the scanner
	// reported for files it could analyse only in part.
	errors map[string]int
	// inSkipped counts the results for listed files that could not be read
	// as text, which the scan does not use.
	inSkipped int
}

// detect runs every scanner over the project folder dir and, when there are
// EUC-KR files, over the folder of copies. A file in the folder of copies
// has the same relative path as its original. A scanner that fails is a
// *toolError.
func (r *Runner) detect(ctx context.Context, dir, copies string, files fileList) (detection, error) {
	d := detection{
		versions: make(map[string]string, len(r.scanners)),
		dropped:  drops{files: map[string]bool{}},
		errors:   make(map[string]int, len(r.scanners)),
	}
	for _, s := range r.scanners {
		v, err := s.Version(ctx)
		if err != nil {
			return detection{}, &toolError{tool: s.Name(), err: fmt.Errorf("reading version: %w", err)}
		}
		d.versions[s.Name()] = v

		folders := []string{dir}
		if files.copied > 0 {
			folders = append(folders, copies)
		}
		for i, folder := range folders {
			raws, counts, err := scanFolder(ctx, s, folder)
			if err != nil {
				return detection{}, &toolError{tool: s.Name(), err: err}
			}
			kept, inSkipped := files.keep(raws, i == 1)
			d.raws = append(d.raws, kept...)
			d.inSkipped += inSkipped
			d.dropped.add(counts)
			d.errors[s.Name()] += counts.Errors
		}
	}
	return d, nil
}

// scanFolder runs s over dir, with counts when s reports them.
func scanFolder(ctx context.Context, s Scanner, dir string) ([]Raw, Counts, error) {
	if c, ok := s.(counter); ok {
		return c.ScanWithCounts(ctx, dir)
	}
	raws, err := s.Scan(ctx, dir)
	return raws, Counts{}, err
}

// keep returns the results that the scan uses, and the number of results it
// left out because their file is listed but could not be read as text.
// fromCopy tells whether the results come from the folder of copies. Results
// it leaves out are not counted as dropped: they are about files the scan
// does not report on, or they repeat a result from the other folder.
//
// A result for a file the scan did not list (excluded, a symbolic link,
// outside the folder) is left out. A whole-file result needs only a listed
// file; it is taken from the project folder, where the file has its original
// bytes. Any other result needs a file read as text: for an EUC-KR file it
// is taken from its UTF-8 copy, and the project folder's result, at EUC-KR
// offsets, is left out.
func (fl fileList) keep(raws []Raw, fromCopy bool) (kept []Raw, inSkipped int) {
	for _, raw := range raws {
		f, listed := fl.files[raw.File]
		switch {
		case !listed:
		case raw.wholeFile():
			if !fromCopy {
				kept = append(kept, raw)
			}
		case f.text && (f.encoding == textfile.EUCKR) == fromCopy:
			kept = append(kept, raw)
		case !f.text:
			inSkipped++
		}
	}
	return kept, inSkipped
}
