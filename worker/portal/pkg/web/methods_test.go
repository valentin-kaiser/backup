package web

import (
	"encoding/json"
	"testing"
)

func TestDescribeOnlyReadsTheListedFields(t *testing.T) {
	s := changes["SetSecret"]
	target, detail := s.describe(json.RawMessage(`{"1":"photos","2":"THE-VALUE","3":false}`))
	if target != "photos" || detail != "" {
		t.Errorf("target = %q, detail = %q", target, detail)
	}
	_, detail = s.describe(json.RawMessage(`{"1":"photos","3":true}`))
	if detail != "generated=true" {
		t.Errorf("detail = %q", detail)
	}
	if target, detail := s.describe(nil); target != "" || detail != "" {
		t.Errorf("no params: %q %q", target, detail)
	}
	if target, _ := s.describe(json.RawMessage(`not json`)); target != "" {
		t.Errorf("garbage: %q", target)
	}
}

func TestEveryChangeHasAnActionAndNoValueFields(t *testing.T) {
	seen := map[string]bool{}
	for name, s := range changes {
		if s.action == "" || len(s.fields) == 0 || seen[s.action] {
			t.Errorf("%s: %+v", name, s)
		}
		seen[s.action] = true
	}
	for _, name := range []string{"SetSecret", "ChangeRepositoryPassword"} {
		for _, f := range changes[name].fields {
			if f.num == "2" {
				t.Errorf("%s records field 2, which is the password", name)
			}
		}
	}
	for _, f := range jobFields {
		if f.num == "5" {
			t.Error("the arguments of a job can hold credentials and must not be recorded")
		}
	}
	for name := range reads {
		if _, ok := changes[name]; ok {
			t.Errorf("%s is both a read and a change", name)
		}
	}
}
