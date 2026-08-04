package rag

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInferType(t *testing.T) {
	cases := map[string]string{
		"readme-tech-news.md":   "readme",
		"README.md":             "readme",
		"blog how raft works":   "blog",
		"draft_designing_apis":  "blog",
		"dynamo.txt":            "whitepaper",
		"notes-from-blogs-1.md": "note",
		"note-on-raft.md":       "note",
		// A note wins over the .txt suffix — otherwise reading notes saved as
		// plain text would be typed as white papers.
		"notes-on-paxos.txt": "note",
		"scratch.pdf":        "unknown",
	}
	for filename, want := range cases {
		if got := inferType(filename); got != want {
			t.Errorf("inferType(%q) = %q, want %q", filename, got, want)
		}
	}
}

func TestReadCorpusFilesStampsTypeAndSkipsHidden(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("readme-foo.md", "some readme")
	writeFile("dynamo.txt", "some paper")
	writeFile(".DS_Store", "junk")
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}

	files, err := readCorpusFiles(dir)
	if err != nil {
		t.Fatalf("readCorpusFiles returned error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files (hidden + dirs skipped), got %d", len(files))
	}

	byName := map[string]corpusFile{}
	for _, f := range files {
		byName[f.Name] = f
	}
	if f := byName["readme-foo.md"]; f.Type != "readme" || f.Content != "some readme" {
		t.Errorf("readme-foo.md read wrong: %+v", f)
	}
	if f := byName["dynamo.txt"]; f.Type != "whitepaper" || f.Content != "some paper" {
		t.Errorf("dynamo.txt read wrong: %+v", f)
	}
}

func TestReadCorpusFilesMissingDir(t *testing.T) {
	if _, err := readCorpusFiles(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("expected error for missing corpus dir")
	}
}
