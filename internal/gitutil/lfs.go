package gitutil

import (
	"bufio"
	"bytes"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/toolpath"
)

// lfsPointerMax bounds a pointer file; real ones are around 130 bytes.
const lfsPointerMax = 1024

var lfsOIDLine = regexp.MustCompile(`(?m)^oid sha256:([0-9a-f]{64})$`)

// LFSPointerOIDs returns every LFS object id referenced by a pointer blob
// anywhere in the repository: every object, not just the reachable ones,
// since an unreachable blob is still an object gc has not removed.
func LFSPointerOIDs(dir string) ([]string, error) {
	list := exec.Command(toolpath.Look("git"), "-C", dir, "cat-file", "--batch-all-objects",
		"--batch-check=%(objecttype) %(objectsize) %(objectname)")
	out, err := list.Output()
	if err != nil {
		return nil, err
	}
	var small []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 3 || f[0] != "blob" {
			continue
		}
		if n, err := strconv.Atoi(f[1]); err == nil && n <= lfsPointerMax {
			small = append(small, f[2])
		}
	}
	if len(small) == 0 {
		return nil, nil
	}
	cat := exec.Command(toolpath.Look("git"), "-C", dir, "cat-file", "--batch")
	cat.Stdin = strings.NewReader(strings.Join(small, "\n") + "\n")
	out, err = cat.Output()
	if err != nil {
		return nil, err
	}
	// --batch output: "<sha> blob <size>\n<content>\n" per object.
	var oids []string
	rest := out
	for len(rest) > 0 {
		nl := bytes.IndexByte(rest, '\n')
		if nl < 0 {
			break
		}
		hdr := strings.Fields(string(rest[:nl]))
		rest = rest[nl+1:]
		if len(hdr) != 3 {
			break
		}
		size, _ := strconv.Atoi(hdr[2])
		if size > len(rest) {
			break
		}
		body := rest[:size]
		rest = rest[size:]
		if len(rest) > 0 && rest[0] == '\n' {
			rest = rest[1:]
		}
		if !bytes.HasPrefix(body, []byte("version https://git-lfs.github.com/spec/")) {
			continue
		}
		if m := lfsOIDLine.FindSubmatch(body); m != nil {
			oids = append(oids, string(m[1]))
		}
	}
	return oids, nil
}
