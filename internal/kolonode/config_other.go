//go:build !unix

package kolonode

import "os"

func openFile(path string) (*os.File, error) { return os.Open(path) }
