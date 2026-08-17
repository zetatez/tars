package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParsePatch(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: /tmp/a.txt\n@@\n context\n-old\n+new\n*** Update File: /tmp/b.txt\n@@\n+added\n*** End Patch"
	files, err := ParsePatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].Path != "/tmp/a.txt" || files[0].Hunks[0].Addition[0] != "context" {
		t.Errorf("bad a.txt parse: %+v", files[0])
	}
	if len(files[0].Hunks[0].Pattern) != 2 { // context + old
		t.Errorf("a.txt pattern should be 2 lines (context+old): %v", files[0].Hunks[0].Pattern)
	}
	if files[1].Path != "/tmp/b.txt" {
		t.Errorf("bad b.txt path: %s", files[1].Path)
	}
}

func TestApplyPatchReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	os.WriteFile(path, []byte("line1\nline2\nline3\n"), 0o644)

	patch := "*** Begin Patch\n*** Update File: " + path + "\n@@\n line2\n-old2\n+new2\n*** End Patch"
	files, err := ParsePatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	lines, ok := applyHunk([]string{"line1", "line2", "old2", "line3"}, files[0].Hunks[0])
	if !ok {
		t.Fatal("hunk should match")
	}
	got := stringsJoin(lines)
	want := "line1\nline2\nnew2\nline3"
	if got != want {
		t.Errorf("apply result mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestApplyPatchCreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	patch := "*** Begin Patch\n*** Update File: " + path + "\n@@\n+hello\n+world\n*** End Patch"
	files, _ := ParsePatch(patch)
	if len(files[0].Hunks[0].Pattern) != 0 {
		t.Fatal("create patch should have no pattern")
	}
	sc := &Scope{Cwd: dir, Cfg: nil}
	_, err := runApplyPatch(t.Context(), map[string]any{"patch": patch}, sc)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "hello\nworld\n" {
		t.Errorf("created file wrong: %q", string(b))
	}
}

func stringsJoin(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}
