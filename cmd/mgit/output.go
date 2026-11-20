package main

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"github.com/MITRAKER/mini-git/internal/bounded"
	"github.com/MITRAKER/mini-git/internal/object"
)

// output buffers what a command prints. Printing can fail too (a full disk,
// an I/O error), so output remembers the first error and flush returns it: a
// command's output is checked once, at the end, instead of after every line
// (coding standard, rule 7). Try `mgit log > /dev/full`. (A closed pipe, as
// in `mgit log | head -1`, is different: like git, mgit is then stopped by
// the SIGPIPE signal, without a message.)
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

// readStdin reads all of standard input, up to mgit's object size limit
// (coding standard, rule 3).
func readStdin() ([]byte, error) {
	return bounded.ReadAll(os.Stdin, object.MaxSize, "standard input")
}

// readFile reads a whole file, up to mgit's object size limit.
func readFile(path string) ([]byte, error) {
	return bounded.ReadFile(path, object.MaxSize)
}
