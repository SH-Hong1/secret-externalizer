package db_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
)

func newMigratedStore(t *testing.T) *db.Store {
	t.Helper()
	s := db.NewTestStore(t)
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return s
}

func TestCreateProject_RoundTrip(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	in := project.Project{Name: "shop", Path: "team/shop", Excludes: []string{"vendor", "build/out"}, CreatedBy: "fake-subject"}

	id, err := s.CreateProject(ctx, in)
	if err != nil {
		t.Fatalf("CreateProject(%+v) error = %v", in, err)
	}
	got, err := s.Project(ctx, id)
	if err != nil {
		t.Fatalf("Project(%d) error = %v", id, err)
	}
	if got.CreatedAt.IsZero() {
		t.Errorf("Project(%d).CreatedAt is zero, want the insert time", id)
	}
	want := in
	want.ID = id
	want.CreatedAt = got.CreatedAt
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Project(%d) mismatch (-want +got):\n%s", id, diff)
	}
}

func TestCreateProject_NoExcludes(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()

	id, err := s.CreateProject(ctx, project.Project{Name: "shop", Path: "shop", CreatedBy: "fake-subject"})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	got, err := s.Project(ctx, id)
	if err != nil {
		t.Fatalf("Project(%d) error = %v", id, err)
	}
	if len(got.Excludes) != 0 {
		t.Errorf("Project(%d).Excludes = %v, want none", id, got.Excludes)
	}
}

func TestCreateProject_Duplicate(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	if _, err := s.CreateProject(ctx, project.Project{Name: "shop", Path: "shop", CreatedBy: "fake-subject"}); err != nil {
		t.Fatalf("CreateProject(first) error = %v", err)
	}
	tests := []struct {
		name string
		in   project.Project
	}{
		{"same name", project.Project{Name: "shop", Path: "other", CreatedBy: "fake-subject"}},
		{"same path", project.Project{Name: "other", Path: "shop", CreatedBy: "fake-subject"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.CreateProject(ctx, tt.in)
			if !errors.Is(err, db.ErrDuplicate) {
				t.Errorf("CreateProject(%+v) error = %v, want ErrDuplicate", tt.in, err)
			}
		})
	}
}

func TestCreateProject_InvalidName(t *testing.T) {
	s := newMigratedStore(t)
	_, err := s.CreateProject(t.Context(), project.Project{Name: "", Path: "shop", CreatedBy: "fake-subject"})
	if err == nil {
		t.Fatal("CreateProject(empty name) error = nil, want an error")
	}
	if errors.Is(err, db.ErrDuplicate) {
		t.Errorf("CreateProject(empty name) error = %v, want an error other than ErrDuplicate", err)
	}
}

func TestProject_NotFound(t *testing.T) {
	s := newMigratedStore(t)
	_, err := s.Project(t.Context(), 424242)
	if !errors.Is(err, db.ErrNotFound) {
		t.Errorf("Project(424242) error = %v, want ErrNotFound", err)
	}
}
