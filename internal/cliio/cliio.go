// Package cliio is terminal plumbing: tty detection and plain-vs-styled
// output decisions.
package cliio

import (
	"os"
)

// IsTTY reports whether f is a character device — the stdlib way, no
// dependency: a pipe or file has ModeCharDevice unset.
func IsTTY(f *os.File) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}
