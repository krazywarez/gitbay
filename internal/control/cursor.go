package control

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/protocol"
)

// Cursor pagination. A cursor is opaque to clients: base64url of
// "<kind>:<key>", where key is the sort key of the last row of the
// previous page. The kind keeps a cursor minted by one command from
// being fed to another.

const maxPageLimit = 200

func encodeCursor(kind, key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(kind + ":" + key))
}

func decodeCursor(kind, cursor string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", errors.New("bad cursor")
	}
	k, key, ok := strings.Cut(string(raw), ":")
	if !ok || k != kind || key == "" {
		return "", errors.New("bad cursor")
	}
	return key, nil
}

// page carries parsed --limit/--cursor flags. active marks that either
// flag was given: only then does the output switch to the paged shape.
type page struct {
	limit  int
	key    string // decoded cursor key, "" means from the start
	active bool
}

// queryLimit is what the store is asked for: one row beyond the page, so
// the presence of a following page is known without a second query.
func (p page) queryLimit() int {
	if p.limit == 0 {
		return 0
	}
	return p.limit + 1
}

// keyInt returns the cursor key as a number; parsePageFlags has already
// validated it for numeric kinds.
func (p page) keyInt() int64 {
	n, _ := strconv.ParseInt(p.key, 10, 64)
	return n
}

// parsePageFlags strips --limit and --cursor from args. kind names the
// cursor namespace; numeric declares the sort key an integer.
func parsePageFlags(c *Ctx, args []string, kind string, numeric bool) (rest []string, p page, code int) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--limit":
			if i+1 >= len(args) {
				return nil, p, c.fail(protocol.ExitUsage, "--limit requires a value")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 1 || n > maxPageLimit {
				return nil, p, c.fail(protocol.ExitUsage, "--limit must be 1 to %d", maxPageLimit)
			}
			p.limit, p.active = n, true
			i++
		case "--cursor":
			if i+1 >= len(args) {
				return nil, p, c.fail(protocol.ExitUsage, "--cursor requires a value")
			}
			key, err := decodeCursor(kind, args[i+1])
			if err == nil && numeric {
				_, err = strconv.ParseInt(key, 10, 64)
			}
			if err != nil {
				return nil, p, c.fail(protocol.ExitUsage, "bad cursor")
			}
			p.key, p.active = key, true
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	return rest, p, -1
}

// trimPage cuts the probe row and derives the next cursor from the last
// row kept.
func trimPage[T any](p page, items []T, kind string, key func(T) string) ([]T, string) {
	if p.limit == 0 || len(items) <= p.limit {
		return items, ""
	}
	items = items[:p.limit]
	return items, encodeCursor(kind, key(items[len(items)-1]))
}

// emitPage renders a list result. Without pagination flags the shape is
// the bare array it has always been; with them the array moves under
// "items" with the next cursor alongside.
func (c *Ctx) emitPage(p page, items any, next string, plain func(w io.Writer)) int {
	if !p.active {
		return c.emit(items, plain)
	}
	if v := reflect.ValueOf(items); v.Kind() == reflect.Slice && v.IsNil() {
		items = reflect.MakeSlice(v.Type(), 0, 0).Interface()
	}
	type out struct {
		Items any    `json:"items"`
		Next  string `json:"next,omitempty"`
	}
	return c.emit(out{items, next}, func(w io.Writer) {
		plain(w)
		if next != "" {
			fmt.Fprintf(w, "next\t%s\n", next)
		}
	})
}
