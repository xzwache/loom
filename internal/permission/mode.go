package permission

import "fmt"

type Mode string

const (
	ModeDefault     Mode = "default"
	ModeAcceptEdits Mode = "acceptEdits"
	ModePlan        Mode = "plan"
	ModeBypass      Mode = "bypassPermissions"
)

// Access is what a call does to the machine. The zero value is not valid, so
// a guard that forgets to set it never counts as read-only.
type Access int

const (
	// ReadOnly calls only read, or change only loom's own state.
	ReadOnly Access = iota + 1
	// Edits change files in the workspace.
	Edits
	// Other is anything else, such as a command that is not known to be safe.
	Other
)

// ModeSpec is what a mode does to each kind of access.
type ModeSpec struct {
	// Override answers before ask and allow rules; only deny rules and the
	// floor come first.
	Override map[Access]Verdict
	// Fallback answers when no rule matches; a missing entry asks.
	Fallback map[Access]Verdict
}

var modes = map[Mode]ModeSpec{
	ModeDefault: {
		Fallback: map[Access]Verdict{ReadOnly: allowed()},
	},
	ModeAcceptEdits: {
		Fallback: map[Access]Verdict{ReadOnly: allowed(), Edits: allowed()},
	},
	ModePlan: {
		Override: map[Access]Verdict{
			Edits: denied("plan mode allows only reading"),
			Other: denied("plan mode allows only reading"),
		},
		Fallback: map[Access]Verdict{ReadOnly: allowed()},
	},
	ModeBypass: {
		Override: map[Access]Verdict{ReadOnly: allowed(), Edits: allowed(), Other: allowed()},
	},
}

// parseMode checks m and defaults an empty mode to ModeDefault.
func parseMode(m Mode) (Mode, error) {
	if m == "" {
		return ModeDefault, nil
	}
	if _, ok := modes[m]; !ok {
		return "", fmt.Errorf("unknown permission mode %q", m)
	}
	return m, nil
}
