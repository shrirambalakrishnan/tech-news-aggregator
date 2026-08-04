package rag

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file owns the INPUT stage of the embed pipeline: everything that knows
// about the corpus directory and its filenames lives here.

const CORPUS_DIR = "profile/corpus"

// corpusFile is one source document, typed at read time so later stages never
// need to inspect filenames.
type corpusFile struct {
	Name    string
	Type    string
	Content string
}

// readCorpusFiles reads every regular, non-hidden file in dir as a corpus
// document, stamping each with its inferred type.
func readCorpusFiles(dir string) ([]corpusFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read corpus dir %s: %w", dir, err)
	}
	var files []corpusFile
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("failed to read corpus file %s: %w", entry.Name(), err)
		}
		files = append(files, corpusFile{
			Name:    entry.Name(),
			Type:    inferType(entry.Name()),
			Content: string(content),
		})
	}
	return files, nil
}

// inferType categorises a corpus file from its name. The processed corpus is a
// flat directory mixing blog posts ("blog ..."/"draft_..."), repo READMEs
// ("readme-..."), white papers (the large .txt files), and the notes the user
// takes while reading ("note-..."/"notes-..."). Best-effort only; Source is the
// authoritative field, and no retrieval or ranking code reads Type at all — it
// exists to make the built index inspectable (e.g. with `jq`).
func inferType(filename string) string {
	name := strings.ToLower(filename)
	switch {
	case strings.HasPrefix(name, "readme"):
		return "readme"
	case strings.HasPrefix(name, "blog"), strings.HasPrefix(name, "draft"):
		return "blog"
	// Matches "note-" and "notes-" alike, so the naming of the reading-notes
	// files need not be settled before they land in the corpus. Deliberately
	// ahead of the .txt case: a "notes-foo.txt" is a note, not a white paper.
	case strings.HasPrefix(name, "note"):
		return "note"
	case strings.HasSuffix(name, ".txt"):
		return "whitepaper"
	default:
		return "unknown"
	}
}
