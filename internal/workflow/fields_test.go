package workflow

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestParseFieldValue(t *testing.T) {
	enum := Field{Type: TypeEnum, Values: []string{"a", "b"}}
	cases := []struct {
		f    Field
		raw  string
		want string // fmt %#v of the result, or "error"
	}{
		{Field{Type: TypeString}, " x, y ", `" x, y "`},
		{Field{Type: TypeText}, "line\nline", `"line\nline"`},
		{Field{Type: TypeInt}, " 42", `42`},
		{Field{Type: TypeInt}, "4.2", "error"},
		{Field{Type: TypeBool}, "true", `true`},
		{Field{Type: TypeBool}, "yes", "error"},
		{enum, "b", `"b"`},
		{enum, "c", "error"},
		{Field{Type: TypeList}, `a, b ,,c`, `[]string{"a", "b", "c"}`},
		{Field{Type: TypeList}, `["x, y", "z"]`, `[]string{"x, y", "z"}`},
		{Field{Type: TypeList}, `["x", 1]`, "error"},
		{Field{Type: TypeList}, ``, `[]string{}`},
		{Field{Type: TypePaths}, `cmd/rig/, internal/workflow/**`, `[]string{"cmd/rig/", "internal/workflow/**"}`},
		{Field{Type: TypePaths}, `["a", " "]`, "error"},
		{Field{Type: TypeTasks}, `t-1,t-2`, `[]string{"t-1", "t-2"}`},
		{Field{Type: "date"}, `x`, "error"},
	}
	for _, c := range cases {
		v, err := ParseFieldValue(c.f, c.raw)
		got := fmt.Sprintf("%#v", v)
		if err != nil {
			got = "error"
		}
		if got != c.want {
			t.Errorf("ParseFieldValue(%s, %q) = %s (%v), want %s", c.f.Type, c.raw, got, err, c.want)
		}
		if err == nil {
			if err := ValidateFieldValue(c.f, v); err != nil {
				t.Errorf("ValidateFieldValue(%s, parsed %q): %v", c.f.Type, c.raw, err)
			}
		}
	}
}

func TestValidateFieldValueFromJSON(t *testing.T) {
	enum := Field{Type: TypeEnum, Values: []string{"a"}}
	cases := []struct {
		f    Field
		json string
		ok   bool
	}{
		{Field{Type: TypeInt}, `3`, true},
		{Field{Type: TypeInt}, `3.5`, false},
		{Field{Type: TypeInt}, `"3"`, false},
		{Field{Type: TypeString}, `3`, false},
		{Field{Type: TypeBool}, `false`, true},
		{enum, `"a"`, true},
		{enum, `"z"`, false},
		{Field{Type: TypeList}, `["a", ""]`, true},
		{Field{Type: TypeTasks}, `["t-1"]`, true},
		{Field{Type: TypeTasks}, `[""]`, false},
		{Field{Type: TypePaths}, `"a"`, false},
		{Field{Type: TypePaths}, `[1]`, false},
	}
	for _, c := range cases {
		var v any
		if err := json.Unmarshal([]byte(c.json), &v); err != nil {
			t.Fatal(err)
		}
		if err := ValidateFieldValue(c.f, v); (err == nil) != c.ok {
			t.Errorf("ValidateFieldValue(%s, %s) = %v, want ok %v", c.f.Type, c.json, err, c.ok)
		}
	}
}
