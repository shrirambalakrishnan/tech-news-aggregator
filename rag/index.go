package rag

import (
	"encoding/json"
	"fmt"
	"os"
)

// This file owns the OUTPUT stage of the embed pipeline: the index artifact's
// schema and its (de)serialization. It knows nothing about corpus sources.

const (
	CORPUS_INDEX_FILE           = "profile/corpus_index.json"
	CORPUS_INDEX_SCHEMA_VERSION = 1
)

// Chunk is one embedded slice of a corpus file. Source is the originating
// filename (authoritative); Type is a best-effort category derived from it.
type Chunk struct {
	Source     string    `json:"source"`
	Type       string    `json:"type"`
	ChunkIndex int       `json:"chunk_index"`
	Text       string    `json:"text"`
	Embedding  []float32 `json:"embedding"`
}

// CorpusIndex is the artifact written to profile/corpus_index.json by the embed
// step and read by the (later) retrieval step. Like user_context.json it is a
// regenerable, git-ignored cache wrapped with provenance metadata
// (SchemaVersion, GeneratedAt, Model, Dimension) for reproducibility.
type CorpusIndex struct {
	SchemaVersion int     `json:"schema_version"`
	GeneratedAt   string  `json:"generated_at"`
	Model         string  `json:"model"`
	Dimension     int     `json:"dimension"`
	ChunkCount    int     `json:"chunk_count"`
	Chunks        []Chunk `json:"chunks"`
}

// WriteCorpusIndex persists the index as indented JSON.
func WriteCorpusIndex(index CorpusIndex, path string) error {
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal corpus index: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write corpus index to %s: %w", path, err)
	}
	return nil
}

// LoadCorpusIndex reads the index from disk. The retrieval step (next) uses this;
// callers should fail soft when it returns an error, since the embed step may not
// have run yet.
func LoadCorpusIndex(path string) (CorpusIndex, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return CorpusIndex{}, fmt.Errorf("failed to read corpus index from %s: %w", path, err)
	}
	var index CorpusIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return CorpusIndex{}, fmt.Errorf("failed to unmarshal corpus index: %w", err)
	}
	return index, nil
}
