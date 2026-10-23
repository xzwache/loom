// Package procgroup runs a command in its own process group, so cancelling
// it also kills what it started (a test runner's children, `sleep &`).
// Elsewhere than Unix it does nothing.
package procgroup
