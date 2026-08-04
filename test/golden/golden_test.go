package golden

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pablo-botella/mkskill/compiler"
)

// TestDocsAreFresh is the golden guard: it composes mskblob's own views in
// memory and compares them against the deployed artifacts — if someone
// edited _mkskill/src without regenerating, this fails and says how to fix
// it. Line endings are normalized so a CRLF checkout doesn't lie.
func TestDocsAreFresh(t *testing.T) {
	base, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	root := &compiler.Root{ProjectBase: base}
	if err := root.Load(); err != nil {
		t.Fatal(err)
	}
	if err := root.Scan(nil); err != nil {
		t.Fatal(err)
	}
	if err := root.Resolve(nil); err != nil {
		t.Fatal(err)
	}

	main := root.ProjectMap["main"]
	cli := root.ProjectMap["cli"]
	checks := []struct {
		unit *compiler.Project
		view string
		file string
	}{
		{main, "readme", "README.md"},
		{main, "agents", "AGENTS.md"},
		{main, "skill", ".claude/skills/mskblob/SKILL.md"},
		{cli, "readme", "cmd/mskblob/README.md"},
	}
	for _, c := range checks {
		var v *compiler.View
		for i := range compiler.Views {
			if compiler.Views[i].Name == c.view {
				v = &compiler.Views[i]
			}
		}
		want, err := c.unit.Compose(io.Discard, v)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(c.file)))
		if err != nil {
			t.Fatalf("%s: %v (stale docs? run: go generate ./...)", c.file, err)
		}
		if normalize(string(data)) != normalize(want) {
			t.Errorf("%s is stale — edit _mkskill/src, then run: go generate ./...", c.file)
		}
	}
}

// normalize levels the line endings: composed output is LF, a checkout may
// carry CRLF — the comparison should care about content only.
func normalize(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}
