package main

import "testing"

// `rig version` takes no argument: an extra one is a usage error.
func TestVersionRejectsArguments(t *testing.T) {
	if r := rig(t, t.TempDir(), nil, nil, "version"); r.code != 0 || r.out != version+"\n" {
		t.Fatalf("version: %+v", r)
	}
	if r := rig(t, t.TempDir(), nil, nil, "version", "extra"); r.code != 2 || r.out != "" || r.errOut == "" {
		t.Fatalf("version extra: %+v", r)
	}
}
