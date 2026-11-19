package scan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"

	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
)

// Store is the part of the database the runner uses.
type Store interface {
	StartScan(ctx context.Context, id ID) error
	Scan(ctx context.Context, id ID) (Scan, error)
	Project(ctx context.Context, id project.ID) (project.Project, error)
	FailScan(ctx context.Context, jobID int64, workerID string, id ID, reason string) error
	FinishEmptyScan(ctx context.Context, jobID int64, workerID string, id ID, files, skipped int) error
}

// The reasons a scan is failed with. They are shown on the scan page, so
// they never contain text from an error.
const (
	reasonOutsideRoot  = "project path points outside the workspace root"
	reasonNotDirectory = "project path is not a directory in the workspace"
	reasonOther        = "scan could not finish; see the worker log"
)

// Runner runs the scan of a claimed job.
type Runner struct {
	logger   *slog.Logger
	store    Store
	root     string
	workerID string
}

// NewRunner returns a Runner that scans projects under root, which is the
// workspace root, and records results as the job holder workerID.
func NewRunner(logger *slog.Logger, store Store, root, workerID string) *Runner {
	return &Runner{logger: logger, store: store, root: root, workerID: workerID}
}

// Handle runs the scan of j and records how it ended. A scan that cannot be
// carried out is failed with a fixed reason and Handle returns nil. Handle
// returns an error, leaving the scan as it is, when ctx ends, when the lease
// on the job is lost, or when the failure itself cannot be recorded.
func (r *Runner) Handle(ctx context.Context, j job.Job) error {
	id := ID(j.ScanID)
	files, err := r.run(ctx, id)
	if err == nil {
		if err := r.store.FinishEmptyScan(ctx, j.ID, r.workerID, id, files, 0); err != nil {
			return fmt.Errorf("finishing scan %d: %w", id, err)
		}
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("running scan %d: %w", id, ctx.Err())
	}

	r.logger.ErrorContext(ctx, "scan failed", "scan_id", id, "job_id", j.ID, "error", err.Error())
	if err := r.store.FailScan(ctx, j.ID, r.workerID, id, reasonFor(err)); err != nil {
		return fmt.Errorf("failing scan %d: %w", id, err)
	}
	return nil
}

// run starts the scan and returns the number of regular files in its
// project folder.
func (r *Runner) run(ctx context.Context, id ID) (int, error) {
	if err := r.store.StartScan(ctx, id); err != nil {
		return 0, fmt.Errorf("starting scan: %w", err)
	}
	sc, err := r.store.Scan(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("reading scan: %w", err)
	}
	p, err := r.store.Project(ctx, sc.ProjectID)
	if err != nil {
		return 0, fmt.Errorf("reading project: %w", err)
	}
	dir, err := project.Resolve(r.root, p.Path)
	if err != nil {
		return 0, fmt.Errorf("resolving project path: %w", err)
	}
	return countFiles(ctx, dir)
}

// countFiles returns the number of regular files under dir. It does not
// follow symbolic links, and it does not count them.
func countFiles(ctx context.Context, dir string) (int, error) {
	files := 0
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.Type().IsRegular() {
			files++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("walking project folder: %w", err)
	}
	return files, nil
}

// reasonFor returns the failure reason shown for err.
func reasonFor(err error) string {
	switch {
	case errors.Is(err, project.ErrOutsideRoot):
		return reasonOutsideRoot
	case errors.Is(err, project.ErrNotDirectory):
		return reasonNotDirectory
	}
	return reasonOther
}
