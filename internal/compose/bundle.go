package compose

import (
	"fmt"
	"os"
)

// The set of local files to be staged and deployed to the remote project directory.
type Bundle struct {
	Files []BundleFile
}

type BundleFile struct {
	LocalPath  string
	RemoteName string
}

func (bundle *Bundle) Validate() error {
	for i, file := range bundle.Files {
		if file.RemoteName == "" {
			return fmt.Errorf("bundle file [%d]: RemoteName must not be empty", i)
		}
		if file.LocalPath == "" {
			return fmt.Errorf("bundle file %q: LocalPath must not be empty", file.RemoteName)
		}
		if _, err := os.Stat(file.LocalPath); err != nil {
			return fmt.Errorf("bundle file %q: %w", file.RemoteName, err)
		}
	}
	return nil
}
