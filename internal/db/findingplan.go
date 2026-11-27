package db

import (
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// storedFinding is a finding row of a project as planFindings reads it.
type storedFinding struct {
	id          int64
	fingerprint string
	file        string
	key         string
	// keyFingerprint is the fingerprint of the key's text, or "" when the
	// row has no key or was stored before key fingerprints were kept.
	keyFingerprint string
	bundle         int64
	// seen says that the scan being saved has stored the finding already.
	seen bool
}

// autoBundle is a shared or frontend bundle row of a project.
type autoBundle struct {
	id          int64
	fingerprint string
	origin      string
}

// bundleRef names a bundle that is stored, by id, or one that the plan
// creates, by planned, its 1-based position in findingPlan.bundles. The
// zero value names no bundle.
type bundleRef struct {
	id      int64
	planned int
}

// plannedBundle is a bundle a plan creates.
type plannedBundle struct {
	fingerprint string
	origin      string
	// decisionFrom is the stored bundle whose decision of the value the new
	// bundle takes, or 0 for none.
	decisionFrom int64
}

// plannedRow is a finding row as a scan leaves it.
type plannedRow struct {
	// id is the stored row, or 0 for a row the plan inserts.
	id int64
	// finding has the key and the values the row gets.
	finding finding.Finding
	// bundle is the bundle a row the plan inserts joins.
	bundle bundleRef
}

// findingPlan is what storing the findings of a scan writes.
type findingPlan struct {
	// bundles are the bundles to create, in order.
	bundles []plannedBundle
	// rows are the finding rows to update or insert, in the order the scan
	// first found them.
	rows []plannedRow
	// slots has, for each finding of the scan, the index of its row in rows.
	slots []int
}

// planRow is a finding row while planFindings works: a stored row, or one
// the plan inserts.
type planRow struct {
	id             int64
	key            string
	keyFingerprint string
	bundle         bundleRef
	// seen says that the scan has found the row.
	seen bool
	// slot is the index of the row in findingPlan.rows, or -1 while the scan
	// has not found it.
	slot int
}

// fileOfValue names one value in one file.
type fileOfValue struct {
	fingerprint string
	file        string
}

// identity names one finding of a project.
type identity struct {
	fileOfValue

	key string
}

// fileOfBundle names one file among the members of a bundle.
type fileOfBundle struct {
	bundle bundleRef
	file   string
}

// autoKey names the shared or the frontend bundle of a fingerprint.
type autoKey struct {
	fingerprint string
	origin      string
}

// planner holds the rows of a project as they are after each finding of a
// scan is planned.
type planner struct {
	plan     findingPlan
	byID     map[identity]*planRow
	byFile   map[fileOfValue][]*planRow
	byBundle map[fileOfBundle][]*planRow
	auto     map[autoKey]bundleRef
	// takenOver has, for each value and file whose keyless row a key took
	// over in this scan, the bundle of that row.
	takenOver map[fileOfValue]int64
	// keyed has each value and file of which the scan found a keyed
	// finding.
	keyed map[fileOfValue]bool
}

// planFindings decides what storing findings, the findings of one scan in
// the order the scan gives them, writes over the stored findings and the
// shared and frontend bundles of the project. stored must hold every finding
// of the project and bundles every bundle that is not an own bundle.
//
// A finding that is stored under the same fingerprint, file and key keeps
// its row. A keyed finding that is not stored takes over the row of its
// fingerprint and file whose key has the same key fingerprint, when the scan
// has not found that row yet: the key's text is the same, and only its form
// changed between the text and its masked form, as values found elsewhere in
// the scan appear or go. Otherwise a keyed finding that is not stored takes
// over the one row of its
// fingerprint and file when that row has no key and the scan has not found
// it yet. A keyless finding that is not stored takes over the one row of its
// fingerprint and file when that row has a key, the scan has not found it
// yet, the scan finds no keyed finding of that fingerprint and file, and the
// finding is not customer data, which has no key and a bundle of its own: a
// scan that cannot read a key it read before keeps the row with its key,
// bundle and decision. Any other finding is a new row, in the bundle bundleFor gives it,
// except that a new row of a value and file whose keyless row was taken
// over in this scan gets an own bundle that takes the decision of the value
// from the taken-over row's bundle (see storeFindings).
// Each finding is planned over the rows as the findings before it left them,
// so the order of findings decides which key takes over a keyless row.
func planFindings(stored []storedFinding, bundles []autoBundle, findings []finding.Finding) findingPlan {
	p := &planner{
		plan:      findingPlan{slots: make([]int, len(findings))},
		byID:      make(map[identity]*planRow, len(stored)),
		byFile:    make(map[fileOfValue][]*planRow, len(stored)),
		byBundle:  make(map[fileOfBundle][]*planRow, len(stored)),
		auto:      make(map[autoKey]bundleRef, len(bundles)),
		takenOver: make(map[fileOfValue]int64),
		keyed:     make(map[fileOfValue]bool),
	}
	for _, f := range findings {
		if f.Key != "" {
			p.keyed[fileOfValue{f.Fingerprint, f.File}] = true
		}
	}
	for _, b := range bundles {
		p.auto[autoKey{b.fingerprint, b.origin}] = bundleRef{id: b.id}
	}
	for _, f := range stored {
		p.add(fileOfValue{f.fingerprint, f.file}, &planRow{
			id: f.id, key: f.key, keyFingerprint: f.keyFingerprint, bundle: bundleRef{id: f.bundle},
			seen: f.seen, slot: -1,
		})
	}
	for i, f := range findings {
		p.plan.slots[i] = p.place(f)
	}
	return p.plan
}

// add puts a row of the given value and file in the indexes.
func (p *planner) add(of fileOfValue, r *planRow) {
	p.byID[identity{of, r.key}] = r
	p.byFile[of] = append(p.byFile[of], r)
	in := fileOfBundle{r.bundle, of.file}
	p.byBundle[in] = append(p.byBundle[in], r)
}

// place plans f and returns the index of its row in the plan.
func (p *planner) place(f finding.Finding) int {
	of := fileOfValue{f.Fingerprint, f.File}
	r, ok := p.byID[identity{of, f.Key}]
	if !ok {
		r, ok = p.reform(of, f)
	}
	if !ok {
		r, ok = p.takeOver(of, f)
	}
	if !ok {
		r = &planRow{key: f.Key, slot: -1}
		if from, taken := p.takenOver[of]; taken {
			r.bundle = p.newBundle(f.Fingerprint, originOwn)
			p.plan.bundles[r.bundle.planned-1].decisionFrom = from
		} else {
			r.bundle = p.bundleFor(f)
		}
		p.add(of, r)
	}
	r.seen = true
	if r.slot < 0 {
		r.slot = len(p.plan.rows)
		p.plan.rows = append(p.plan.rows, plannedRow{id: r.id, bundle: r.bundle})
	}
	if f.Key != "" {
		r.keyFingerprint = f.KeyFingerprint
	}
	f.Key, f.KeyFingerprint = r.key, r.keyFingerprint
	p.plan.rows[r.slot].finding = f
	return r.slot
}

// reform returns the row of the value and file of f, a keyed finding that
// is not stored, whose key is the other form of the same key text: the same
// key fingerprint under another key. The scan must not have found the row
// yet. The row takes the key of f.
func (p *planner) reform(of fileOfValue, f finding.Finding) (*planRow, bool) {
	if f.Key == "" || f.KeyFingerprint == "" {
		return nil, false
	}
	for _, r := range p.byFile[of] {
		if r.seen || r.key == "" || r.keyFingerprint != f.KeyFingerprint {
			continue
		}
		delete(p.byID, identity{of, r.key})
		r.key = f.Key
		p.byID[identity{of, f.Key}] = r
		return r, true
	}
	return nil, false
}

// takeOver returns the one row of the value and file of f, a finding that
// is not stored, when the row can be taken over as
// planFindings describes. A keyed finding gives a keyless row its key and
// starts the value decisions of the file's other keys from its bundle. A
// keyless finding that is not customer data takes over a keyed row only
// when the scan finds no key of the value and file, and leaves the row its key: the key holds no value,
// since a key that overlaps one is masked rather than dropped, and a later
// key that differs still starts a row of its own.
func (p *planner) takeOver(of fileOfValue, f finding.Finding) (*planRow, bool) {
	rows := p.byFile[of]
	if len(rows) != 1 || rows[0].seen {
		return nil, false
	}
	r := rows[0]
	keyedOverKeyless := f.Key != "" && r.key == ""
	keylessOverKeyed := f.Key == "" && r.key != "" && !p.keyed[of] &&
		f.Category != finding.CategoryCustomerData
	if !keyedOverKeyless && !keylessOverKeyed {
		return nil, false
	}
	if keylessOverKeyed {
		// The keyless finding names the row from now on, so that the
		// scan's other keyless findings of the file meet it.
		p.byID[identity{of, ""}] = r
		return r, true
	}
	p.takenOver[of] = r.bundle.id
	delete(p.byID, identity{of, ""})
	r.key = f.Key
	p.byID[identity{of, f.Key}] = r
	return r, true
}

// bundleFor returns the bundle a finding that is stored for the first time
// joins, planning a new one when needed. Customer data and kinds that do not
// share a bundle get a new own bundle; a finding in a Frontend file joins the
// frontend bundle of its fingerprint, and any other the shared one.
//
// A finding gets a new own bundle instead when the shared or frontend bundle
// holds a finding of the same file under another key, the empty key
// included, whichever scan found it. A new key of a file therefore never
// takes over the decision and name given to another key of that file, even
// one that is gone from the source or already applied.
func (p *planner) bundleFor(f finding.Finding) bundleRef {
	if f.Category == finding.CategoryCustomerData || !triage.SharesBundle(f.Kind) {
		return p.newBundle(f.Fingerprint, originOwn)
	}
	origin := originShared
	if triage.Frontend(f.File) {
		origin = originFrontend
	}
	ref, ok := p.auto[autoKey{f.Fingerprint, origin}]
	if !ok {
		ref = p.newBundle(f.Fingerprint, origin)
		p.auto[autoKey{f.Fingerprint, origin}] = ref
		return ref
	}
	for _, member := range p.byBundle[fileOfBundle{ref, f.File}] {
		if member.key != f.Key {
			return p.newBundle(f.Fingerprint, originOwn)
		}
	}
	return ref
}

// newBundle plans a bundle and returns its reference.
func (p *planner) newBundle(fingerprint, origin string) bundleRef {
	p.plan.bundles = append(p.plan.bundles, plannedBundle{fingerprint: fingerprint, origin: origin})
	return bundleRef{planned: len(p.plan.bundles)}
}
