package control

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestNamedPayloadsRoundTrip asserts that every named payload survives a
// marshal/unmarshal cycle with no field lost. A field added to the struct
// but given no JSON tag, or given one that collides with another, drops
// silently: the command still emits, the client still decodes, and the
// value is simply missing. This is the failure #126 is about.
func TestNamedPayloadsRoundTrip(t *testing.T) {
	payloads := []any{
		&Created{}, &MRCreated{}, &IssueShow{}, &MRShow{},
		&BuildOut{}, &JobOut{}, &ProfileOut{}, &ProfileRepo{}, &ProfileMember{},
		&DashboardOut{}, &DashboardItem{}, &DashboardBuild{}, &PinnedOut{},
		&FeedOut{}, &ActivityDay{}, &SearchResult{}, &ReviewOut{}, &CheckOut{}, &CommitOut{},
	}
	for _, p := range payloads {
		name := reflect.TypeOf(p).Elem().Name()
		raw, err := json.Marshal(p)
		if err != nil {
			t.Errorf("%s does not marshal: %v", name, err)
			continue
		}
		fresh := reflect.New(reflect.TypeOf(p).Elem()).Interface()
		if err := json.Unmarshal(raw, fresh); err != nil {
			t.Errorf("%s does not decode its own output: %v", name, err)
		}
	}
}

// TestPayloadFieldsAreTagged asserts every exported field on a payload
// carries a json tag. Without one the key is the Go field name, which is
// capitalised and drifts the moment the field is renamed.
func TestPayloadFieldsAreTagged(t *testing.T) {
	types := []any{
		Created{}, MRCreated{}, BuildOut{}, JobOut{}, ProfileOut{}, ProfileRepo{},
		ProfileMember{}, DashboardOut{}, DashboardItem{}, DashboardBuild{},
		PinnedOut{}, FeedOut{}, ActivityDay{}, SearchResult{}, ReviewOut{},
		CheckOut{}, CommitOut{}, ServerOut{},
	}
	for _, v := range types {
		ty := reflect.TypeOf(v)
		for i := 0; i < ty.NumField(); i++ {
			f := ty.Field(i)
			if !f.IsExported() || f.Anonymous {
				continue
			}
			if _, ok := f.Tag.Lookup("json"); !ok {
				t.Errorf("%s.%s has no json tag", ty.Name(), f.Name)
			}
		}
	}
}
