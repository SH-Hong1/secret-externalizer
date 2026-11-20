package rewrite

import "os"

// SetBeforeCommit makes d call f right before it renames a temporary file
// over the file it replaces or links one to the name of a new file.
func SetBeforeCommit(d *Dir, f func()) { d.beforeCommit = f }

// SetSyncDir makes d flush a folder after a change with f.
func SetSyncDir(d *Dir, f func(*os.File) error) { d.syncDir = f }
