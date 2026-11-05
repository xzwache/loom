package task

import (
	"fmt"
	"strings"
)

// ListLimit caps the tasks one list call shows, so a long list cannot fill
// the context.
const ListLimit = 30

// Group is how a task stands: what list sorts and filters by.
type Group string

const (
	GroupInProgress Group = "in_progress"
	GroupReady      Group = "ready"
	GroupWaiting    Group = "waiting"
	GroupCompleted  Group = "completed"
)

// listOrder puts what needs attention first: work under way, then work that
// can start.
var listOrder = []Group{GroupInProgress, GroupReady, GroupWaiting}

func groupOf(all map[string]Task, t Task) Group {
	switch {
	case t.Status == InProgress:
		return GroupInProgress
	case t.Status == Completed:
		return GroupCompleted
	case len(waitingOn(all, t)) > 0:
		return GroupWaiting
	default:
		return GroupReady
	}
}

func byID(tasks []Task) map[string]Task {
	all := make(map[string]Task, len(tasks))
	for _, t := range tasks {
		all[t.ID] = t
	}
	return all
}

// Counts is how many tasks are in each group.
type Counts map[Group]int

func Count(tasks []Task) Counts {
	all := byID(tasks)
	c := Counts{}
	for _, t := range tasks {
		c[groupOf(all, t)]++
	}
	return c
}

func (c Counts) Open() int { return c[GroupInProgress] + c[GroupReady] + c[GroupWaiting] }

// Summary is one line for the system prompt about the open tasks of list, or
// "" when there are none.
func Summary(list string, tasks []Task) string {
	c := Count(tasks)
	if c.Open() == 0 {
		return ""
	}
	noun := "tasks"
	if c.Open() == 1 {
		noun = "task"
	}
	return fmt.Sprintf("# Tasks\n\nThe task list %q has %d open %s: %d in progress, %d ready, %d waiting. "+
		"Call the task tool with action list to see them before you start work they may cover.",
		list, c.Open(), noun, c[GroupInProgress], c[GroupReady], c[GroupWaiting])
}

// Render is the list result: the tasks of the given group, or every open
// task in listOrder when group is empty, at most ListLimit of them, and a
// count of what it leaves out.
func Render(tasks []Task, group Group) string {
	all := byID(tasks)
	groups := listOrder
	if group != "" {
		groups = []Group{group}
	}
	var lines []string
	shown := Counts{}
	for _, g := range groups {
		for _, t := range tasks {
			if groupOf(all, t) != g || len(lines) == ListLimit {
				continue
			}
			lines = append(lines, line(t, waitingOn(all, t)))
			shown[g]++
		}
	}
	if len(lines) == 0 {
		lines = append(lines, "No "+describe(group)+".")
	}
	c := Count(tasks)
	var hidden []string
	for _, g := range groups {
		if n := c[g] - shown[g]; n > 0 {
			hidden = append(hidden, fmt.Sprintf("%d %s", n, g))
		}
	}
	if len(hidden) > 0 {
		lines = append(lines, fmt.Sprintf("(not shown: %s; list with a status to see them)", strings.Join(hidden, ", ")))
	}
	if group == "" && c[GroupCompleted] > 0 {
		lines = append(lines, fmt.Sprintf("(%d completed; list with status completed to see them)", c[GroupCompleted]))
	}
	return strings.Join(lines, "\n")
}

func describe(g Group) string {
	if g == "" {
		return "open tasks"
	}
	return string(g) + " tasks"
}

// line is one task in a list: [ ] ready, [-] waiting, [>] in progress,
// [x] completed.
func line(t Task, waiting []string) string {
	mark := map[Status]string{Pending: "[ ]", InProgress: "[>]", Completed: "[x]"}[t.Status]
	if t.Status == Pending && len(waiting) > 0 {
		mark = "[-]"
	}
	out := fmt.Sprintf("%s %s %s", mark, t.ID, t.Subject)
	switch {
	case t.Status == InProgress:
		out += " (owner " + t.Owner + ")"
	case len(waiting) > 0:
		out += " (waiting on " + strings.Join(waiting, ", ") + ")"
	}
	return out
}
