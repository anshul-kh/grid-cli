//go:build windows

package extensions

import "errors"

// spawnIgnoredOrphan is unix-only: the orphan test gates itself off on
// Windows rather than re-deriving a job-object equivalent the runner's own
// windows tests already cover.
func spawnIgnoredOrphan() error {
	return errors.New("orphan helper is unix-only")
}
