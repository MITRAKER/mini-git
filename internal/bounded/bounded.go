// Package bounded reads files and streams with an upper limit on their size,
// so that a huge or endless input cannot make mgit use all the memory
// (coding standard, rule 3). Every file mgit reads goes through it.
package bounded

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// ReadAll reads r to the end, refusing more than limit bytes. name says what
// r is, for the error message.
func ReadAll(r io.Reader, limit int64, name string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, limit)
	}
	return data, nil
}

// ReadFile reads a whole file, refusing one larger than limit bytes. When the
// file cannot be opened, the error is os.Open's, so errors.Is(err,
// os.ErrNotExist) still tells that it does not exist.
func ReadFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, readErr := ReadAll(f, limit, path)
	if err := errors.Join(readErr, f.Close()); err != nil {
		return nil, err
	}
	return data, nil
}
