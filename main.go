// Command grid is the companion CLI for Grid lessons: it scaffolds lesson
// workspaces, measures checks locally, and submits evidence to the server
// for a verdict. The CLI measures; the server judges.
package main

import (
	"os"

	"github.com/anshul-kh/grid-cli/cmd"
)

// main is the only place this program calls os.Exit (golang-cli review
// checklist item 2) — every command underneath returns an error from RunE
// instead, and cmd.Execute() maps that to a TASK.md §3 exit code.
func main() {
	os.Exit(cmd.Execute())
}
