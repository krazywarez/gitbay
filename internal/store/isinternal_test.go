package store

import (
	"errors"
	"testing"
)

func TestIsInternal(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	_, err := s.DB.Exec("INSERT INTO no_such_table (x) VALUES (1)")
	if err == nil || !IsInternal(err) {
		t.Errorf("a SQLite error is internal: %v", err)
	}
	for _, e := range []error{ErrNotFound, ErrExists, ErrDuplicateKey, errors.New("name must be lowercase")} {
		if IsInternal(e) {
			t.Errorf("%v is not internal", e)
		}
	}
}
