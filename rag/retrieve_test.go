package rag

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestCosineSimilarity(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"same direction ignores magnitude", []float32{1, 0}, []float32{5, 0}, 1},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0},
		{"opposite", []float32{1, 0}, []float32{-1, 0}, -1},
		{"zero vector", []float32{0, 0}, []float32{1, 0}, 0},
		{"length mismatch", []float32{1}, []float32{1, 0}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cosineSimilarity(c.a, c.b); math.Abs(got-c.want) > 1e-9 {
				t.Errorf("cosineSimilarity(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

func TestTopKBySimilarity(t *testing.T) {
	chunks := []Chunk{
		{Text: "far", Embedding: []float32{0, 1}},       // sim 0 to query
		{Text: "closest", Embedding: []float32{1, 0}},   // sim 1
		{Text: "close", Embedding: []float32{0.6, 0.8}}, // sim 0.6
	}
	query := []float32{1, 0}

	t.Run("ranks best-first and truncates to k", func(t *testing.T) {
		got := topKBySimilarity(chunks, query, 2)
		if len(got) != 2 || got[0].Text != "closest" || got[1].Text != "close" {
			t.Errorf("top 2 = %+v, want [closest close]", got)
		}
	})

	t.Run("k larger than chunk count returns all", func(t *testing.T) {
		if got := topKBySimilarity(chunks, query, 10); len(got) != 3 {
			t.Errorf("expected all 3 chunks, got %d", len(got))
		}
	})
}

func TestRetrieveContext(t *testing.T) {
	t.Run("returns top-k chunk texts, best match first", func(t *testing.T) {
		originalLoad, originalEmbed := loadCorpusIndex, embedQuery
		defer func() { loadCorpusIndex, embedQuery = originalLoad, originalEmbed }()

		loadCorpusIndex = func(path string) (CorpusIndex, error) {
			return CorpusIndex{Chunks: []Chunk{
				{Text: "far", Embedding: []float32{0, 1}},
				{Text: "closest", Embedding: []float32{1, 0}},
				{Text: "close", Embedding: []float32{0.6, 0.8}},
			}}, nil
		}
		var gotQuery string
		embedQuery = func(query string) ([]float32, error) {
			gotQuery = query
			return []float32{1, 0}, nil
		}

		texts, err := RetrieveContext("story titles here", 2)
		if err != nil {
			t.Fatalf("RetrieveContext returned error: %v", err)
		}
		if gotQuery != "story titles here" {
			t.Errorf("query embedded = %q", gotQuery)
		}
		if !reflect.DeepEqual(texts, []string{"closest", "close"}) {
			t.Errorf("texts = %v, want [closest close]", texts)
		}
	})

	t.Run("errors when the index is missing, without embedding", func(t *testing.T) {
		originalLoad, originalEmbed := loadCorpusIndex, embedQuery
		defer func() { loadCorpusIndex, embedQuery = originalLoad, originalEmbed }()

		loadCorpusIndex = func(path string) (CorpusIndex, error) {
			return CorpusIndex{}, errors.New("no such file")
		}
		embedQuery = func(query string) ([]float32, error) {
			t.Fatal("embedQuery should not be called when the index is missing")
			return nil, nil
		}

		if _, err := RetrieveContext("q", 2); err == nil {
			t.Error("expected error when index is missing")
		}
	})

	t.Run("errors when query embedding fails", func(t *testing.T) {
		originalLoad, originalEmbed := loadCorpusIndex, embedQuery
		defer func() { loadCorpusIndex, embedQuery = originalLoad, originalEmbed }()

		loadCorpusIndex = func(path string) (CorpusIndex, error) {
			return CorpusIndex{Chunks: []Chunk{{Text: "a", Embedding: []float32{1}}}}, nil
		}
		embedQuery = func(query string) ([]float32, error) {
			return nil, errors.New("rate limited")
		}

		if _, err := RetrieveContext("q", 1); err == nil {
			t.Error("expected error when query embedding fails")
		}
	})
}
