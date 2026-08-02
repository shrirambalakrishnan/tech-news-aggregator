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

func TestTopKAboveFloor(t *testing.T) {
	chunks := []Chunk{
		{Text: "far", Embedding: []float32{0, 1}},       // sim 0 to query
		{Text: "closest", Embedding: []float32{1, 0}},   // sim 1
		{Text: "close", Embedding: []float32{0.6, 0.8}}, // sim 0.6
	}
	query := []float32{1, 0}

	texts := func(scored []scoredChunk) []string {
		out := []string{}
		for _, s := range scored {
			out = append(out, s.chunk.Text)
		}
		return out
	}

	t.Run("drops chunks below the floor", func(t *testing.T) {
		got := texts(topKAboveFloor(chunks, query, 5, 0.5))
		if !reflect.DeepEqual(got, []string{"closest", "close"}) {
			t.Errorf("got %v, want [closest close] (sim 0 chunk filtered)", got)
		}
	})

	t.Run("returns empty when everything is below the floor", func(t *testing.T) {
		weak := []Chunk{chunks[0], chunks[2]} // sims 0 and 0.6 — no strong match
		if got := topKAboveFloor(weak, query, 5, 0.9); len(got) != 0 {
			t.Errorf("expected no chunks above a 0.9 floor, got %v", texts(got))
		}
	})

	t.Run("still truncates to k when many are above the floor", func(t *testing.T) {
		got := texts(topKAboveFloor(chunks, query, 1, 0.0))
		if !reflect.DeepEqual(got, []string{"closest"}) {
			t.Errorf("got %v, want [closest]", got)
		}
	})

	t.Run("carries the scores for pooling", func(t *testing.T) {
		got := topKAboveFloor(chunks, query, 1, 0.5)
		if len(got) != 1 || math.Abs(got[0].score-1) > 1e-9 {
			t.Errorf("expected the top chunk scored 1, got %+v", got)
		}
	})
}

func TestPoolChunks(t *testing.T) {
	// Same chunk (a,0) retrieved by two stories at different scores; (b,1) and
	// (b,2) are distinct windows of the same source and must both survive.
	a0 := Chunk{Source: "a.md", ChunkIndex: 0, Text: "a0"}
	b1 := Chunk{Source: "b.md", ChunkIndex: 1, Text: "b1"}
	b2 := Chunk{Source: "b.md", ChunkIndex: 2, Text: "b2"}

	texts := func(chunks []Chunk) []string {
		out := []string{}
		for _, c := range chunks {
			out = append(out, c.Text)
		}
		return out
	}

	t.Run("dedupes on source+index, keeping the max score, best first", func(t *testing.T) {
		perStory := [][]scoredChunk{
			{{chunk: a0, score: 0.4}, {chunk: b1, score: 0.9}},
			{{chunk: a0, score: 0.95}, {chunk: b2, score: 0.5}}, // a0 again, higher
		}
		got := texts(poolChunks(perStory, 10))
		// a0's best score (0.95) beats b1 (0.9), so the later, better hit wins.
		if !reflect.DeepEqual(got, []string{"a0", "b1", "b2"}) {
			t.Errorf("got %v, want [a0 b1 b2]", got)
		}
	})

	t.Run("truncates at the cap", func(t *testing.T) {
		perStory := [][]scoredChunk{{{chunk: a0, score: 0.9}, {chunk: b1, score: 0.8}, {chunk: b2, score: 0.7}}}
		if got := texts(poolChunks(perStory, 2)); !reflect.DeepEqual(got, []string{"a0", "b1"}) {
			t.Errorf("got %v, want [a0 b1]", got)
		}
	})

	t.Run("empty per-story results pool to nothing", func(t *testing.T) {
		if got := poolChunks([][]scoredChunk{{}, {}}, 10); len(got) != 0 {
			t.Errorf("expected an empty pool, got %v", texts(got))
		}
	})
}

func TestRetrievePooledContext(t *testing.T) {
	index := CorpusIndex{Chunks: []Chunk{
		{Source: "a.md", ChunkIndex: 0, Text: "east", Embedding: []float32{1, 0}},
		{Source: "b.md", ChunkIndex: 0, Text: "north", Embedding: []float32{0, 1}},
	}}

	t.Run("one query per story, pooled best-first", func(t *testing.T) {
		originalLoad, originalEmbed := loadCorpusIndex, embedQueries
		originalK, originalCap := RETRIEVAL_TOP_K_PER_STORY, RETRIEVAL_POOL_CAP
		defer func() {
			loadCorpusIndex, embedQueries = originalLoad, originalEmbed
			RETRIEVAL_TOP_K_PER_STORY, RETRIEVAL_POOL_CAP = originalK, originalCap
		}()
		RETRIEVAL_TOP_K_PER_STORY, RETRIEVAL_POOL_CAP = 1, 10

		loadCorpusIndex = func(path string) (CorpusIndex, error) { return index, nil }
		var gotQueries []string
		embedQueries = func(queries []string) ([][]float32, error) {
			gotQueries = queries
			// story 1 points east, stories 2 and 3 point north
			return [][]float32{{1, 0}, {0, 1}, {0, 1}}, nil
		}

		stories := []string{"story one", "story two", "story three"}
		texts, err := RetrievePooledContext(stories)
		if err != nil {
			t.Fatalf("RetrievePooledContext returned error: %v", err)
		}
		if !reflect.DeepEqual(gotQueries, stories) {
			t.Errorf("queries embedded = %v, want one per story %v", gotQueries, stories)
		}
		// "north" is retrieved twice but appears once; both chunks score 1, and
		// "east" was seen first, so it leads.
		if !reflect.DeepEqual(texts, []string{"east", "north"}) {
			t.Errorf("texts = %v, want [east north]", texts)
		}
	})

	t.Run("floor can empty the pool entirely", func(t *testing.T) {
		originalLoad, originalEmbed := loadCorpusIndex, embedQueries
		originalFloor := RETRIEVAL_SIMILARITY_FLOOR
		defer func() {
			loadCorpusIndex, embedQueries = originalLoad, originalEmbed
			RETRIEVAL_SIMILARITY_FLOOR = originalFloor
		}()
		RETRIEVAL_SIMILARITY_FLOOR = 0.99

		loadCorpusIndex = func(path string) (CorpusIndex, error) { return index, nil }
		embedQueries = func(queries []string) ([][]float32, error) {
			return [][]float32{{0.6, 0.8}}, nil // best sim 0.8, below the floor
		}

		texts, err := RetrievePooledContext([]string{"unrelated story"})
		if err != nil {
			t.Fatalf("RetrievePooledContext returned error: %v", err)
		}
		if len(texts) != 0 {
			t.Errorf("expected no excerpts when nothing clears the floor, got %v", texts)
		}
	})

	t.Run("errors when the index is missing, without embedding", func(t *testing.T) {
		originalLoad, originalEmbed := loadCorpusIndex, embedQueries
		defer func() { loadCorpusIndex, embedQueries = originalLoad, originalEmbed }()

		loadCorpusIndex = func(path string) (CorpusIndex, error) {
			return CorpusIndex{}, errors.New("no such file")
		}
		embedQueries = func(queries []string) ([][]float32, error) {
			t.Fatal("embedQueries should not be called when the index is missing")
			return nil, nil
		}

		if _, err := RetrievePooledContext([]string{"q"}); err == nil {
			t.Error("expected error when index is missing")
		}
	})

	t.Run("errors when query embedding fails", func(t *testing.T) {
		originalLoad, originalEmbed := loadCorpusIndex, embedQueries
		defer func() { loadCorpusIndex, embedQueries = originalLoad, originalEmbed }()

		loadCorpusIndex = func(path string) (CorpusIndex, error) { return index, nil }
		embedQueries = func(queries []string) ([][]float32, error) {
			return nil, errors.New("rate limited")
		}

		if _, err := RetrievePooledContext([]string{"q"}); err == nil {
			t.Error("expected error when query embedding fails")
		}
	})

	t.Run("errors when the embedder returns the wrong count", func(t *testing.T) {
		originalLoad, originalEmbed := loadCorpusIndex, embedQueries
		defer func() { loadCorpusIndex, embedQueries = originalLoad, originalEmbed }()

		loadCorpusIndex = func(path string) (CorpusIndex, error) { return index, nil }
		embedQueries = func(queries []string) ([][]float32, error) {
			return [][]float32{{1, 0}}, nil // one vector for two queries
		}

		if _, err := RetrievePooledContext([]string{"a", "b"}); err == nil {
			t.Error("expected error on query/vector count mismatch")
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
