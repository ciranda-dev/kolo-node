//go:build unix

package kolonode

import (
	"os"
	"syscall"
)

// openFile opens path without blocking and without taking a controlling terminal, so a FIFO or device swapped in
// after the path check is opened harmlessly and then refused by the check of the open file.
func openFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
}
