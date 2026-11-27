package scan

import (
	"bytes"
	"cmp"
	"slices"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// classified is a detection together with the category and kind its rule
// gave.
type classified struct {
	finding.Detection
	category finding.Category
	kind     finding.Kind
}

// findingKey identifies a finding within a file: a value under a
// configuration key.
type findingKey struct {
	fp, key string
}

// mergedLocation is the results that found one value at one place.
type mergedLocation struct {
	spot     spot
	fp       string
	detected []classified
	// keyAt is where the location's key is read: the smallest offset that
	// placedFile.keyAt gives any of its results, so that the key does not
	// depend on the order the scanners reported them in.
	keyAt int
}

// merge turns the placed results into findings. Results in the same file,
// at the same start, with the same fingerprint are one location; locations
// in the same file with the same fingerprint and configuration key are one
// finding. fingerprints maps each value to its fingerprint.
//
// It also returns the originals to keep, by fingerprint: the value of every
// finding that is not customer data. No finding in a file marked noContext
// gets surrounding lines (see placedFile); that covers every file with a
// customer data finding and every file on a customer data path. Surrounding
// lines mask every placed value of the file, every place an unplaced
// result's value may be, and every place in the file of a value in elsewhere,
// the values found anywhere in the scan. elsewhere is searched only in files
// that have a finding with surrounding lines.
//
// A location's end is the line and column of the value's last character.
// Findings are ordered by file, first location, fingerprint, and key; locations
// by position. A value inside a URL takes the key of the URL
// (placedFile.keyAt). A key is empty in a file without surrounding lines, for a
// whole-file result, for customer data, and for a value that spans lines.
// When a piece of the key overlaps a range masked in surrounding lines, the
// key is finding.MaskedKey of the key's fingerprint, so that a key never
// holds a value found in the scan. spans are those ranges per file of placed,
// from maskedSpans, and fingerprints holds the text of every such key, from
// maskedKeys.
func merge(placed []*placedFile, fingerprints map[string]string, spans [][]finding.Span) ([]finding.Finding, map[string][]byte) {
	var findings []finding.Finding
	originals := map[string][]byte{}
	for i, pf := range placed {
		spans := spans[i]
		type locationKey struct {
			start int
			fp    string
		}
		locations := map[locationKey]*mergedLocation{}
		var order []*mergedLocation
		for _, s := range pf.spots {
			fp := fingerprints[string(s.value)]
			lk := locationKey{start: s.span.Start, fp: fp}
			at := pf.keyAt(s)
			ml, ok := locations[lk]
			if !ok {
				ml = &mergedLocation{spot: s, fp: fp, keyAt: at}
				locations[lk] = ml
				order = append(order, ml)
			}
			ml.keyAt = min(ml.keyAt, at)
			ml.detected = append(ml.detected, classified{
				Detection: finding.Detection{Tool: s.raw.Tool, Rule: s.raw.Rule, Encoding: s.raw.Encoding},
				category:  s.raw.Category, kind: s.raw.Kind,
			})
		}
		byFinding := map[findingKey][]*mergedLocation{}
		for _, ml := range order {
			key, masked := spotKey(pf, ml.spot, ml.keyAt, spans)
			if masked {
				key = maskedKey(fingerprints, key)
			}
			fk := findingKey{fp: ml.fp, key: key}
			byFinding[fk] = append(byFinding[fk], ml)
		}

		for fk, mls := range byFinding {
			f := newFinding(pf, fk, mls, spans)
			if f.HasOriginal {
				originals[fk.fp] = mls[0].spot.value
			}
			findings = append(findings, f)
		}
	}
	slices.SortFunc(findings, func(a, b finding.Finding) int {
		return cmp.Or(
			cmp.Compare(a.File, b.File),
			cmp.Compare(a.Locations[0].StartLine, b.Locations[0].StartLine),
			cmp.Compare(a.Locations[0].StartCol, b.Locations[0].StartCol),
			cmp.Compare(a.Fingerprint, b.Fingerprint),
			cmp.Compare(a.Key, b.Key),
		)
	})
	return findings, originals
}

// maskedSpans returns, per file of placed, the ranges masked in its
// surrounding lines: every placed value of the file and every place in it of
// a value in elsewhere. A file without surrounding lines has none.
func maskedSpans(placed []*placedFile, elsewhere valueIndex) [][]finding.Span {
	out := make([][]finding.Span, len(placed))
	for i, pf := range placed {
		if pf.showsContext() {
			out[i] = append(pf.spans(), elsewhere.find(pf.file.Text)...)
		}
	}
	return out
}

// maskedKeys returns the text of every key in placed that overlaps a range
// in spans, the result of maskedSpans, once each. merge stores such a key as
// the masked form of its fingerprint.
func maskedKeys(placed []*placedFile, spans [][]finding.Span) [][]byte {
	seen := map[string]bool{}
	var out [][]byte
	for i, pf := range placed {
		for _, s := range pf.spots {
			key, masked := spotKey(pf, s, pf.keyAt(s), spans[i])
			if masked && !seen[key] {
				seen[key] = true
				out = append(out, []byte(key))
			}
		}
	}
	return out
}

// maskedKey returns the masked form of the key text key, or "" when
// fingerprints has no fingerprint for it.
func maskedKey(fingerprints map[string]string, key string) string {
	fp, ok := fingerprints[key]
	if !ok {
		return ""
	}
	return finding.MaskedKey(fp)
}

// spotKey returns the configuration key of s in pf, read at byte at, or ""
// when s gets none. spans are the ranges masked in the file's surrounding
// lines; when a piece of the key overlaps one of them, the key holds a value
// and masked is true.
func spotKey(pf *placedFile, s spot, at int, spans []finding.Span) (key string, masked bool) {
	if !pf.showsContext() || s.raw.wholeFile() || s.raw.Category == finding.CategoryCustomerData ||
		bytes.IndexByte(s.value, '\n') >= 0 {
		return "", false
	}
	key, pieces := configKey(pf.rel, pf.file.Text, at)
	for _, p := range pieces {
		for _, m := range spans {
			if p.Start < m.End && m.Start < p.End {
				return key, true
			}
		}
	}
	return key, false
}

// newFinding builds the finding of the value and key fk in pf from
// its locations. spans are all the value ranges in the file, which are
// masked in the surrounding lines.
func newFinding(pf *placedFile, fk findingKey, mls []*mergedLocation, spans []finding.Span) finding.Finding {
	var all []classified
	for _, ml := range mls {
		all = append(all, ml.detected...)
	}
	category, kind := classify(all)
	f := finding.Finding{
		Fingerprint: fk.fp,
		File:        pf.rel,
		Key:         fk.key,
		Category:    category,
		Kind:        kind,
		Preview:     finding.Preview(category, kind, string(mls[0].spot.value)),
		HasOriginal: category != finding.CategoryCustomerData,
		Shell:       triage.ShellFile(pf.rel, pf.file.Text),
	}
	if category == finding.CategoryCustomerData {
		f.Key = ""
	}
	for _, ml := range mls {
		l := finding.Location{FileHash: pf.hash, Detections: detections(ml.detected)}
		l.StartLine, l.StartCol, l.EndLine, l.EndCol = pf.position(ml.spot)
		if category != finding.CategoryCustomerData && !ml.spot.raw.wholeFile() && !pf.noContext {
			l.Context, l.ContextFirstLine = finding.Context(pf.file, ml.spot.span, spans)
		}
		f.Locations = append(f.Locations, l)
	}
	slices.SortFunc(f.Locations, func(a, b finding.Location) int {
		return cmp.Or(cmp.Compare(a.StartLine, b.StartLine), cmp.Compare(a.StartCol, b.StartCol))
	})
	return f
}

// categoryRank orders the categories: when rules of different categories
// find the same value, the first category in this order wins.
func categoryRank(c finding.Category) int {
	switch c {
	case finding.CategoryStorage:
		return 0
	case finding.CategorySSH:
		return 1
	case finding.CategoryPartnerSession:
		return 2
	case finding.CategoryCustomerData:
		return 3
	case finding.CategoryOther:
		return 4
	}
	return 5
}

// classify returns the category of a finding found by detected, and the
// kind given by a detection of that category: the first, in tool and rule
// order, whose kind is not "other", or else the first.
func classify(detected []classified) (finding.Category, finding.Kind) {
	sorted := slices.Clone(detected)
	slices.SortFunc(sorted, compareDetection)
	best := sorted[0].category
	for _, d := range sorted {
		if categoryRank(d.category) < categoryRank(best) {
			best = d.category
		}
	}
	kind := finding.Kind("")
	for _, d := range sorted {
		if d.category != best {
			continue
		}
		if kind == "" {
			kind = d.kind
		}
		if d.kind != finding.KindOther {
			return best, d.kind
		}
	}
	return best, kind
}

// detections returns the distinct detections in tool and rule order.
func detections(detected []classified) []finding.Detection {
	sorted := slices.Clone(detected)
	slices.SortFunc(sorted, compareDetection)
	out := make([]finding.Detection, 0, len(sorted))
	for _, d := range sorted {
		if n := len(out); n > 0 && out[n-1] == d.Detection {
			continue
		}
		out = append(out, d.Detection)
	}
	return out
}

func compareDetection(a, b classified) int {
	return cmp.Or(
		cmp.Compare(a.Tool, b.Tool),
		cmp.Compare(a.Rule, b.Rule),
		cmp.Compare(a.Encoding, b.Encoding),
		cmp.Compare(categoryRank(a.category), categoryRank(b.category)),
		cmp.Compare(a.kind, b.kind),
	)
}
