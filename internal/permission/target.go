package permission

// target is what a rule's specifier, the part in parentheses, is compared
// with: a shell command, a file, or nothing for tools without one. Most
// targets have one form; a symlink has two, the link and the file it points
// to, which is why fitting some and fitting every form differ.
type target interface {
	// fitsSome reports whether r fits at least one form: enough to deny or ask.
	fitsSome(r Rule) bool
	// fitsEvery reports whether r fits every form: needed to allow.
	fitsEvery(r Rule) bool
}

// anyCall is the target of tools without a specifier: only bare rules fit.
type anyCall struct{}

func (anyCall) fitsSome(r Rule) bool  { return r.spec == "" }
func (anyCall) fitsEvery(r Rule) bool { return r.spec == "" }
