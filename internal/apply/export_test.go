package apply

// ProjectDir is a project folder as the runner uses it.
type ProjectDir = projectDir

// WrapDir makes r use wrap(d) in place of each project folder d it opens.
func WrapDir(r *Runner, wrap func(ProjectDir) ProjectDir) { r.wrapDir = wrap }
