package render

import (
	"testing"

	toon "github.com/toon-format/toon-go"
)

func TestTOONCanonicalShapes(t *testing.T) {
	type row struct {
		ID   string `toon:"id"`
		Name string `toon:"name"`
	}
	type named struct {
		Spans []row `toon:"spans"`
	}
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"object", row{"a", "worker"}, "id: a\nname: worker"},
		{"named rows", named{[]row{{"a", "worker"}, {"b", "task"}}}, "spans[2]{id,name}:\n  a,worker\n  b,task"},
		{"bare rows", []row{{"a", "worker"}, {"b", "task"}}, "[2]{id,name}:\n  a,worker\n  b,task"},
		{"single", []row{{"a", "worker"}}, "[1]{id,name}:\n  a,worker"},
		{"empty", []row{}, "[]"},
		{"nested", struct {
			Child row `toon:"child"`
		}{row{"a", "worker"}}, "child:\n  id: a\n  name: worker"},
		{"nonuniform", []any{row{"a", "worker"}, "text"}, "[2]:\n  - id: a\n    name: worker\n  - text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := toon.MarshalString(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("actual canonical output:\n%s\nexpected:\n%s", got, tc.want)
			}
		})
	}
}
