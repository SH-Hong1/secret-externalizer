package scan

import (
	"cmp"
	"slices"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
)

// classified is a detection together with the category and kind its rule
// gave.
type classified struct {
	finding.Detection
	category finding.Category
	kind     finding.Kind
}

// mergedLocation is the results that found one value at one place.
type mergedLocation struct {
	spot     spot
	detected []classified
}

// merge turns the placed results into findings. Results in the same file,
// at the same start, with the same fingerprint are one location; locations
// in the same file with the same fingerprint are one finding. fingerprints
// maps each value to its fingerprint.
//
// It also returns the originals to keep, by fingerprint: the value of every
// finding that is not customer data. A customer data finding gets no
// surrounding lines.
//
// A location's end is the line and column of the value's last character.
// Findings are ordered by file, first location, and fingerprint; locations
// by position.
func merge(placed []*placedFile, fingerprints map[string]string) ([]finding.Finding, map[string][]byte) {
	var findings []finding.Finding
	originals := map[string][]byte{}
	for _, pf := range placed {
		type locationKey struct {
			start int
			fp    string
		}
		locations := map[locationKey]*mergedLocation{}
		byFingerprint := map[string][]*mergedLocation{}
		for _, s := range pf.spots {
			fp := fingerprints[string(s.value)]
			key := locationKey{start: s.span.Start, fp: fp}
			ml, ok := locations[key]
			if !ok {
				ml = &mergedLocation{spot: s}
				locations[key] = ml
				byFingerprint[fp] = append(byFingerprint[fp], ml)
			}
			ml.detected = append(ml.detected, classified{
				Detection: finding.Detection{Tool: s.raw.Tool, Rule: s.raw.Rule},
				category:  s.raw.Category, kind: s.raw.Kind,
			})
		}

		spans := pf.spans()
		for fp, mls := range byFingerprint {
			f := newFinding(pf, fp, mls, spans)
			if f.HasOriginal {
				originals[fp] = mls[0].spot.value
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
		)
	})
	return findings, originals
}

// newFinding builds the finding of the value with fingerprint fp in pf from
// its locations. spans are all the value ranges in the file, which are
// masked in the surrounding lines.
func newFinding(pf *placedFile, fp string, mls []*mergedLocation, spans []finding.Span) finding.Finding {
	var all []classified
	for _, ml := range mls {
		all = append(all, ml.detected...)
	}
	category, kind := classify(all)
	f := finding.Finding{
		Fingerprint: fp,
		File:        pf.rel,
		Category:    category,
		Kind:        kind,
		Preview:     finding.Preview(category, kind, string(mls[0].spot.value)),
		HasOriginal: category != finding.CategoryCustomerData,
	}
	for _, ml := range mls {
		l := finding.Location{FileHash: pf.hash, Detections: detections(ml.detected)}
		l.StartLine, l.StartCol, l.EndLine, l.EndCol = pf.position(ml.spot)
		if category != finding.CategoryCustomerData && !ml.spot.raw.wholeFile() {
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
		cmp.Compare(categoryRank(a.category), categoryRank(b.category)),
		cmp.Compare(a.kind, b.kind),
	)
}
