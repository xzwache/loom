// Package skill gives the model skills: instructions, and optionally scripts
// and reference files, for one kind of task. A skill is a folder with a
// SKILL.md file in Claude Code's format:
//
//	pdf/
//	  SKILL.md        front matter (name, description) + instructions
//	  forms.md        reference files the instructions point to
//	  scripts/fill.py scripts the model can run
//
// Only each skill's name and description reach the model up front, listed in
// the skill tool's description. When a task matches one, the model calls the
// tool and gets the instructions; it reads the other files through the tool
// as well. So many skills cost little until one is used.
package skill

// FileName is the file that defines a skill inside its folder.
const FileName = "SKILL.md"

type Skill struct {
	Name         string
	Description  string
	Instructions string
	// Dir is the skill's folder; its other files are read relative to it.
	Dir string
}
