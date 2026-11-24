package db_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
)

func TestPlanFindings(t *testing.T) {
	const (
		file  = "conf/db.properties"
		other = "conf/app.properties"
		page  = "web/login.jsp"
	)
	password := func(fp, file, key string) finding.Finding {
		return finding.Finding{
			Fingerprint: fp, File: file, Key: key,
			Category: finding.CategoryStorage, Kind: finding.KindPassword,
		}
	}
	tests := []struct {
		name     string
		stored   []db.StoredFinding
		bundles  []db.AutoBundle
		findings []finding.Finding
		want     []db.Planned
	}{
		{
			name:     "a first finding starts the shared bundle of its fingerprint",
			findings: []finding.Finding{password(fpA, file, "")},
			want:     []db.Planned{{NewBundle: 1, Origin: "shared"}},
		},
		{
			name:     "two files of one value share the bundle the first one starts",
			findings: []finding.Finding{password(fpA, file, ""), password(fpA, other, "")},
			want:     []db.Planned{{NewBundle: 1, Origin: "shared"}, {NewBundle: 1, Origin: "shared"}},
		},
		{
			name:     "a frontend file starts the frontend bundle",
			findings: []finding.Finding{password(fpA, file, ""), password(fpA, page, "")},
			want:     []db.Planned{{NewBundle: 1, Origin: "shared"}, {NewBundle: 2, Origin: "frontend"}},
		},
		{
			name: "a stored finding keeps its row",
			stored: []db.StoredFinding{
				{ID: 7, Fingerprint: fpA, File: file, Key: "db.password", Bundle: 3},
			},
			bundles:  []db.AutoBundle{{ID: 3, Fingerprint: fpA, Origin: "shared"}},
			findings: []finding.Finding{password(fpA, file, "db.password")},
			want:     []db.Planned{{ID: 7, Key: "db.password"}},
		},
		{
			name: "a new file joins the stored shared bundle",
			stored: []db.StoredFinding{
				{ID: 7, Fingerprint: fpA, File: file, Key: "db.password", Bundle: 3},
			},
			bundles:  []db.AutoBundle{{ID: 3, Fingerprint: fpA, Origin: "shared"}},
			findings: []finding.Finding{password(fpA, other, "db.password")},
			want:     []db.Planned{{Key: "db.password", Bundle: 3}},
		},
		{
			name: "the first key of a file takes over its sole keyless finding",
			stored: []db.StoredFinding{
				{ID: 7, Fingerprint: fpA, File: file, Bundle: 3},
			},
			bundles: []db.AutoBundle{{ID: 3, Fingerprint: fpA, Origin: "shared"}},
			findings: []finding.Finding{
				password(fpA, file, "local.password"),
				password(fpA, file, "dev.password"),
			},
			want: []db.Planned{
				{ID: 7, Key: "local.password"},
				{Key: "dev.password", NewBundle: 1, Origin: "own"},
			},
		},
		{
			name: "a keyless finding found first is not taken over",
			stored: []db.StoredFinding{
				{ID: 7, Fingerprint: fpA, File: file, Bundle: 3},
			},
			bundles: []db.AutoBundle{{ID: 3, Fingerprint: fpA, Origin: "shared"}},
			findings: []finding.Finding{
				password(fpA, file, ""),
				password(fpA, file, "dev.password"),
			},
			want: []db.Planned{
				{ID: 7},
				{Key: "dev.password", NewBundle: 1, Origin: "own"},
			},
		},
		{
			name: "a keyless finding after the key that took its row is new",
			stored: []db.StoredFinding{
				{ID: 7, Fingerprint: fpA, File: file, Bundle: 3},
			},
			bundles: []db.AutoBundle{{ID: 3, Fingerprint: fpA, Origin: "shared"}},
			findings: []finding.Finding{
				password(fpA, file, "dev.password"),
				password(fpA, file, ""),
			},
			want: []db.Planned{
				{ID: 7, Key: "dev.password"},
				{NewBundle: 1, Origin: "own"},
			},
		},
		{
			name: "a keyless finding this scan already stored is not taken over",
			stored: []db.StoredFinding{
				{ID: 7, Fingerprint: fpA, File: file, Bundle: 3, Seen: true},
			},
			bundles:  []db.AutoBundle{{ID: 3, Fingerprint: fpA, Origin: "shared"}},
			findings: []finding.Finding{password(fpA, file, "dev.password")},
			want:     []db.Planned{{Key: "dev.password", NewBundle: 1, Origin: "own"}},
		},
		{
			name: "a keyless finding is not taken over when the file has another stored key",
			stored: []db.StoredFinding{
				{ID: 7, Fingerprint: fpA, File: file, Bundle: 3},
				{ID: 8, Fingerprint: fpA, File: file, Key: "local.password", Bundle: 4},
			},
			bundles:  []db.AutoBundle{{ID: 3, Fingerprint: fpA, Origin: "shared"}},
			findings: []finding.Finding{password(fpA, file, "dev.password")},
			want:     []db.Planned{{Key: "dev.password", NewBundle: 1, Origin: "own"}},
		},
		{
			name: "two new keys of one file do not share a bundle",
			findings: []finding.Finding{
				password(fpA, file, "local.password"),
				password(fpA, file, "dev.password"),
				password(fpA, other, "dev.password"),
			},
			want: []db.Planned{
				{Key: "local.password", NewBundle: 1, Origin: "shared"},
				{Key: "dev.password", NewBundle: 2, Origin: "own"},
				{Key: "dev.password", NewBundle: 1, Origin: "shared"},
			},
		},
		{
			name: "a member of another fingerprint under another key keeps a new key out",
			stored: []db.StoredFinding{
				{ID: 7, Fingerprint: fpB, File: file, Key: "local.password", Bundle: 3},
			},
			bundles:  []db.AutoBundle{{ID: 3, Fingerprint: fpA, Origin: "shared"}},
			findings: []finding.Finding{password(fpA, file, "dev.password")},
			want:     []db.Planned{{Key: "dev.password", NewBundle: 1, Origin: "own"}},
		},
		{
			name: "kinds that do not share and customer data get a bundle each",
			findings: []finding.Finding{
				{Fingerprint: fpA, File: file, Category: finding.CategoryStorage, Kind: finding.KindHost},
				{Fingerprint: fpA, File: other, Category: finding.CategoryStorage, Kind: finding.KindHost},
				{Fingerprint: fpB, File: file, Category: finding.CategoryCustomerData, Kind: finding.KindPhone},
			},
			want: []db.Planned{
				{NewBundle: 1, Origin: "own"},
				{NewBundle: 2, Origin: "own"},
				{NewBundle: 3, Origin: "own"},
			},
		},
		{
			name: "a finding given twice is one row with the later values",
			findings: []finding.Finding{
				password(fpA, file, "db.password"),
				password(fpA, file, "db.password"),
			},
			want: []db.Planned{
				{Key: "db.password", NewBundle: 1, Origin: "shared"},
				{Key: "db.password", NewBundle: 1, Origin: "shared"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, _ := db.PlanFindings(tt.stored, tt.bundles, tt.findings)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("planFindings() (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPlanFindings_OneRowPerFinding(t *testing.T) {
	f := finding.Finding{
		Fingerprint: fpA, File: "conf/db.properties", Key: "db.password",
		Category: finding.CategoryStorage, Kind: finding.KindPassword, Preview: "old-****",
	}
	later := f
	later.Preview = "new-****"

	got, rows, bundles := db.PlanFindings(nil, nil, []finding.Finding{f, later})

	if rows != 1 || bundles != 1 {
		t.Fatalf("PlanFindings(one finding twice) = %d rows and %d bundles, want 1 and 1", rows, bundles)
	}
	for i, p := range got {
		if p.Preview != "new-****" {
			t.Errorf("PlanFindings(one finding twice)[%d] preview = %q, want the later %q", i, p.Preview, "new-****")
		}
	}
}
