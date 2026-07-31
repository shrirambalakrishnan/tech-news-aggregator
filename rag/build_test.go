package rag

import (
	"reflect"
	"testing"

	"github.com/shrirambalakrishnan/tech-news/voyageapi"
)

func TestBuildIndexWiresEmbeddingsToChunks(t *testing.T) {
	original := embedDocuments
	defer func() { embedDocuments = original }()

	var captured []string
	embedDocuments = func(texts []string) ([][]float32, error) {
		captured = texts
		out := make([][]float32, len(texts))
		for i := range texts {
			out[i] = []float32{float32(i), 1.0} // deterministic 2-dim vector
		}
		return out, nil
	}

	files := []corpusFile{
		{Name: "readme-foo.md", Type: "readme", Content: "alpha beta gamma"},         // 1 chunk
		{Name: "dynamo.txt", Type: "whitepaper", Content: "one two three four five"}, // 1 chunk
	}

	index, err := buildIndex(files)
	if err != nil {
		t.Fatalf("buildIndex returned error: %v", err)
	}

	if index.ChunkCount != 2 {
		t.Fatalf("expected 2 chunks, got %d", index.ChunkCount)
	}
	if !reflect.DeepEqual(captured, []string{"alpha beta gamma", "one two three four five"}) {
		t.Errorf("texts sent to embedder = %v", captured)
	}
	if index.Dimension != 2 {
		t.Errorf("expected dimension 2, got %d", index.Dimension)
	}
	if index.Model != voyageapi.VOYAGE_EMBEDDING_MODEL {
		t.Errorf("expected model %q, got %q", voyageapi.VOYAGE_EMBEDDING_MODEL, index.Model)
	}

	c0 := index.Chunks[0]
	if c0.Source != "readme-foo.md" || c0.Type != "readme" || c0.ChunkIndex != 0 {
		t.Errorf("chunk 0 metadata wrong: %+v", c0)
	}
	if !reflect.DeepEqual(c0.Embedding, []float32{0, 1}) {
		t.Errorf("chunk 0 embedding = %v, want [0 1]", c0.Embedding)
	}

	c1 := index.Chunks[1]
	if c1.Source != "dynamo.txt" || c1.Type != "whitepaper" || c1.ChunkIndex != 0 {
		t.Errorf("chunk 1 metadata wrong: %+v", c1)
	}
	if !reflect.DeepEqual(c1.Embedding, []float32{1, 1}) {
		t.Errorf("chunk 1 embedding = %v, want [1 1]", c1.Embedding)
	}
}

func TestBuildIndexNoChunks(t *testing.T) {
	original := embedDocuments
	defer func() { embedDocuments = original }()
	embedDocuments = func(texts []string) ([][]float32, error) {
		t.Fatal("embedDocuments should not be called when there are no chunks")
		return nil, nil
	}

	if _, err := buildIndex([]corpusFile{{Name: "empty.md", Content: "   "}}); err == nil {
		t.Error("expected error when no chunks are produced")
	}
}

func TestBuildIndexEmbeddingCountMismatch(t *testing.T) {
	original := embedDocuments
	defer func() { embedDocuments = original }()
	embedDocuments = func(texts []string) ([][]float32, error) {
		return [][]float32{{1}}, nil // fewer than the chunks produced
	}

	files := []corpusFile{{Name: "a.md", Content: "one"}, {Name: "b.md", Content: "two"}}
	if _, err := buildIndex(files); err == nil {
		t.Error("expected error on embedding/chunk count mismatch")
	}
}
