package workflow

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// ParseFieldValue reads a command-line value for a field of type f.Type:
//
//   - string, text: the value as given;
//   - int: a decimal integer; bool: true or false (and strconv's other
//     spellings);
//   - enum: one of f.Values;
//   - list, paths, tasks: a JSON array of strings, or else items separated
//     by commas, trimmed, with empty items dropped. A list item that holds a
//     comma needs the JSON form.
//
// The result is a string, int, bool or []string, ready to record.
func ParseFieldValue(f Field, raw string) (any, error) {
	switch f.Type {
	case TypeString, TypeText:
		return raw, nil
	case TypeInt:
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer", raw)
		}
		return n, nil
	case TypeBool:
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("%q is not true or false", raw)
		}
		return b, nil
	case TypeEnum:
		if !slices.Contains(f.Values, raw) {
			return nil, fmt.Errorf("%q is not one of %s", raw, strings.Join(f.Values, ", "))
		}
		return raw, nil
	case TypeList, TypePaths, TypeTasks:
		var items []string
		if strings.HasPrefix(strings.TrimSpace(raw), "[") {
			if err := json.Unmarshal([]byte(raw), &items); err != nil {
				return nil, fmt.Errorf("%q is not a JSON array of strings", raw)
			}
		} else {
			for _, s := range strings.Split(raw, ",") {
				if s = strings.TrimSpace(s); s != "" {
					items = append(items, s)
				}
			}
		}
		if items == nil {
			items = []string{}
		}
		if err := ValidateFieldValue(f, items); err != nil {
			return nil, err
		}
		return items, nil
	}
	return nil, fmt.Errorf("unknown field type %q", f.Type)
}

// ValidateFieldValue checks that v, as ParseFieldValue gives it or as it
// reads back from JSON, has the type field f declares.
func ValidateFieldValue(f Field, v any) error {
	switch f.Type {
	case TypeString, TypeText:
		if _, ok := v.(string); !ok {
			return fmt.Errorf("want a string, got %T", v)
		}
	case TypeInt:
		switch n := v.(type) {
		case int:
		case float64:
			if n != math.Trunc(n) || math.IsInf(n, 0) {
				return fmt.Errorf("%v is not an integer", n)
			}
		default:
			return fmt.Errorf("want an integer, got %T", v)
		}
	case TypeBool:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("want true or false, got %T", v)
		}
	case TypeEnum:
		s, ok := v.(string)
		if !ok || !slices.Contains(f.Values, s) {
			return fmt.Errorf("%v is not one of %s", v, strings.Join(f.Values, ", "))
		}
	case TypeList, TypePaths, TypeTasks:
		var items []string
		switch l := v.(type) {
		case []string:
			items = l
		case []any:
			for _, e := range l {
				s, ok := e.(string)
				if !ok {
					return fmt.Errorf("list item %v is not a string", e)
				}
				items = append(items, s)
			}
		default:
			return fmt.Errorf("want a list of strings, got %T", v)
		}
		if f.Type != TypeList {
			for _, s := range items {
				if strings.TrimSpace(s) == "" {
					return fmt.Errorf("a %s item is empty", f.Type)
				}
			}
		}
	default:
		return fmt.Errorf("unknown field type %q", f.Type)
	}
	return nil
}
