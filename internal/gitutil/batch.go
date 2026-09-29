package gitutil

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/toolpath"
)

// BlobBatch reads blobs by object id through one `git cat-file --batch`
// process, for a caller reading many blobs in one request.
type BlobBatch struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

// NewBlobBatch starts the cat-file process in dir. Close ends it.
func NewBlobBatch(dir string) (*BlobBatch, error) {
	cmd := exec.Command(toolpath.Look("git"), "-C", dir, "cat-file", "--batch")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &BlobBatch{cmd: cmd, in: in, out: bufio.NewReader(out)}, nil
}

// Read returns the blob oid, refusing one larger than limit bytes.
func (b *BlobBatch) Read(oid string, limit int64) ([]byte, error) {
	if strings.ContainsAny(oid, " \n") {
		return nil, fmt.Errorf("bad object id %q", oid)
	}
	if _, err := io.WriteString(b.in, oid+"\n"); err != nil {
		return nil, err
	}
	head, err := b.out.ReadString('\n')
	if err != nil {
		return nil, err
	}
	f := strings.Fields(head)
	if len(f) != 3 {
		return nil, fmt.Errorf("cat-file %s: %s", oid, strings.TrimSpace(head))
	}
	size, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("cat-file %s: %s", oid, strings.TrimSpace(head))
	}
	// A refused object is still on the stream, with its trailing
	// newline; skipping it keeps the next Read in step.
	if f[1] != "blob" || size > limit {
		if _, err := io.CopyN(io.Discard, b.out, size+1); err != nil {
			return nil, err
		}
		if f[1] != "blob" {
			return nil, fmt.Errorf("%s is a %s, not a blob", oid, f[1])
		}
		return nil, fmt.Errorf("%s is larger than %d bytes", oid, limit)
	}
	data := make([]byte, size+1) // the object and its trailing newline
	if _, err := io.ReadFull(b.out, data); err != nil {
		return nil, err
	}
	return data[:size], nil
}

// Close ends the process.
func (b *BlobBatch) Close() error {
	b.in.Close()
	return b.cmd.Wait()
}
