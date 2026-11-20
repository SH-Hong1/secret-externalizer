package rewrite

// SetBeforeRename makes d call f right before it renames a temporary file
// over the file it replaces.
func SetBeforeRename(d *Dir, f func()) { d.beforeRename = f }
