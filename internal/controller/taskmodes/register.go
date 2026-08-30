package taskmodes

// init registers the built-in task modes. New modes can be added by:
//   1. Implementing TaskModeHandler in a new file in this package.
//   2. Calling Register() below.
//
// Downstream repos that want to add their own modes should import this
// package and call Register from their main package's init().

func init() {
	Register(NewSidecarDrivenHandler())
	Register(NewHarnessHandler())
}
