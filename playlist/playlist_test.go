package playlist

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSkipsCommentsAndBlankLines(t *testing.T) {
	entries, err := Parse("# a comment\n\nradio1\thttp://radio1.com/1.mp3\n   \n")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Name != "radio1" || entries[0].URL != "http://radio1.com/1.mp3" {
		t.Fatalf("got %+v", entries[0])
	}
}

func TestParseContentType(t *testing.T) {
	entries, err := Parse("jazz\thttp://jazz.example/stream\taudio/aac\n")
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].ContentType != "audio/aac" {
		t.Fatalf("got %q", entries[0].ContentType)
	}
}

func TestParseRejectsBadLines(t *testing.T) {
	for _, text := range []string{
		"radio1\thttp://a.com/1.mp3\textra\tmore\n",
		"radio1\tftp://a.com/1.mp3\n",
		"radio1\tnotaurl\n",
		"\thttp://a.com/1.mp3\n",
		"radio1\t\n",
	} {
		if _, err := Parse(text); err == nil {
			t.Errorf("Parse(%q) = nil error, want one", text)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "playlist.txt")

	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Len() != 0 {
		t.Fatal("a missing file should load an empty list")
	}
	if err := p.Add(Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Add(Entry{Name: "jazz", URL: "http://jazz.example/stream", ContentType: "audio/aac"}); err != nil {
		t.Fatal(err)
	}

	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := again.Entries()
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[1].ContentType != "audio/aac" {
		t.Fatalf("content type lost: %+v", got[1])
	}
}

func TestUpdateRemoveMove(t *testing.T) {
	p, err := Load(filepath.Join(t.TempDir(), "p.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", "c"} {
		if err := p.Add(Entry{Name: name, URL: "http://x.com/" + name}); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Update(0, Entry{Name: "A", URL: "http://x.com/A"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Remove(1); err != nil {
		t.Fatal(err)
	}
	// Move the last entry to the front.
	if err := p.Move(1, 0); err != nil {
		t.Fatal(err)
	}
	got := p.Entries()
	var names []string
	for _, e := range got {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "c,A" {
		t.Fatalf("got %v, want [c A]", names)
	}
	if err := p.Update(9, Entry{Name: "z", URL: "http://x.com/z"}); err == nil {
		t.Error("out of range update should fail")
	}
	if err := p.Remove(-1); err == nil {
		t.Error("out of range remove should fail")
	}
}

func TestSaveIsAtomicAndCreatesDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "playlist.txt")
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Add(Entry{Name: "a", URL: "http://x.com/a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not written: %v", err)
	}
	// No temporary file is left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d files in the directory, want 1", len(entries))
	}
}

func TestMoveAcrossTheEnd(t *testing.T) {
	p, _ := Load(filepath.Join(t.TempDir(), "p.txt"))
	for _, name := range []string{"a", "b", "c"} {
		p.Add(Entry{Name: name, URL: "http://x.com/" + name})
	}
	if err := p.Move(0, 3); err != nil {
		t.Fatal(err)
	}
	got := p.Entries()
	var names []string
	for _, e := range got {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "b,c,a" {
		t.Fatalf("got %v, want [b c a]", names)
	}
}
