package main

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDecodeConfigAcceptsQuotedReasoningEquals(t *testing.T) {
	raw := []byte(`reasoning_equals:
  - "516"
  - "1034"
  - "1552"
  - "2070"
`)
	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decodeConfig returned error: %v", err)
	}
	want := intList{516, 1034, 1552, 2070}
	if !reflect.DeepEqual(cfg.ReasoningEquals, want) {
		t.Fatalf("ReasoningEquals = %#v, want %#v", cfg.ReasoningEquals, want)
	}
}

func TestIntListAcceptsCommaSeparatedScalar(t *testing.T) {
	var got intList
	if err := yaml.Unmarshal([]byte(`"516, 1034, 1552"`), &got); err != nil {
		t.Fatalf("yaml.Unmarshal returned error: %v", err)
	}
	want := intList{516, 1034, 1552}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestIntListRejectsNonNumericStrings(t *testing.T) {
	var got intList
	if err := yaml.Unmarshal([]byte(`["516", "oops"]`), &got); err == nil {
		t.Fatal("expected an error for non-numeric strings")
	}
}
