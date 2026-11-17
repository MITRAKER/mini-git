package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/MITRAKER/mini-git/internal/object"
)

// output buffers what a command prints. Printing can fail too (a full disk,
// a closed pipe), so output remembers the first error and flush returns it:
// a command's output is checked once, at the end, instead of after every
// line (coding standard, rule 7). Try `mgit log > /dev/full`.
type output struct {
	w   *bufio.Writer
	err error
}

func newOutput(w io.Writer) *output { return &output{w: bufio.NewWriter(w)} }

// printf formats and prints, like fmt.Printf.
func (o *output) printf(format string, args ...any) {
	if o.err == nil {
		_, o.err = fmt.Fprintf(o.w, format, args...)
	}
}

// write prints bytes as they are.
func (o *output) write(b []byte) {
	if o.err == nil {
		_, o.err = o.w.Write(b)
	}
}

// flush prints what is still buffered and returns the first error.
func (o *output) flush() error {
	if o.err != nil {
		return o.err
	}
	return o.w.Flush()
}

// readLimited reads all of r, up to mgit's object size limit (coding
// standard, rule 3). name says what r is, for the error message.
func readLimited(r io.Reader, name string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, object.MaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > object.MaxSize {
		return nil, fmt.Errorf("%s is larger than mgit's %d byte limit", name, object.MaxSize)
	}
	return data, nil
}

// readFileLimited reads a whole file, up to mgit's object size limit.
func readFileLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, readErr := readLimited(f, path)
	if err := errors.Join(readErr, f.Close()); err != nil {
		return nil, err
	}
	return data, nil
}
