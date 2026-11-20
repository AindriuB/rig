package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
	if err := os.WriteFile(filepath.Join(root, File), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLoadMissingFileIsDefaults(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil || !reflect.DeepEqual(c, Default()) {
		t.Fatalf("Load = %+v, %v", c, err)
	}
	if !reflect.DeepEqual(c.PushPrefixes, []string{"task", "record"}) {
		t.Fatalf("prefixes = %v", c.PushPrefixes)
	}
}

func TestLoad(t *testing.T) {
	c, err := Load(write(t, `{"forge":"gitlab","host":"git.example.com","pushPrefixes":["task","wip"]}`))
	want := Config{Forge: "gitlab", Host: "git.example.com", PushPrefixes: []string{"task", "wip"}}
	if err != nil || !reflect.DeepEqual(c, want) {
		t.Fatalf("Load = %+v, %v", c, err)
	}
	c, err = Load(write(t, `{"forge":"github"}`))
	if err != nil || !reflect.DeepEqual(c.PushPrefixes, DefaultPushPrefixes) {
		t.Fatalf("Load = %+v, %v", c, err)
	}
}

func TestLoadRejects(t *testing.T) {
	for _, body := range []string{
		`{"forge":"bitbucket"}`,
		`{"forg":"github"}`,
		`{"pushPrefixes":["task/"]}`,
		`{"pushPrefixes":[""]}`,
		`{"pushPrefixes":["-f"]}`,
		`{"pushPrefixes":["a:b"]}`,
		`{"pushPrefixes":["+x"]}`,
		`not json`,
	} {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("Load(%s) = nil error", body)
		}
	}
}
