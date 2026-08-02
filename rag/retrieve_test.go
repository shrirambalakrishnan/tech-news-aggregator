package rag

import (
	"errors"
	"fmt"
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

// The two retrieval arms are documented as differing in excerpt SELECTION. They
// also differ in excerpt VOLUME, which is a second variable: the excerpts are
// inlined verbatim into the prompt, so arm 3's larger budget makes its prompt
// several times bigger, and prompt size is itself a plausible cause of any eval
// delta (dilution, position effects). This test runs both arms' real entry
// points at their SHIPPED constants and pins that gap, so the confound cannot
// quietly stop matching what README and CLAUDE.md say about it.
//
// If this test fails because the constants were equalized: good - the confound
// is gone, but the recorded arm 2 vs arm 3 numbers were measured under the old
// ones. Re-run both arms and drop the confound caveats from README/CLAUDE.md
// before deleting this test.
//
// It guards ONE of the two ways the gap can close: changing the constants. The
// other - changing the k arm 2 asks for, at the call site in armcontext - is
// invisible here, because that k is armcontext's choice and rag cannot import
// armcontext; armcontext's own TestBuildProfileArmRAG already asserts on that k
// and carries the same warning.
//
// What only this test can see is behavioural drift in the pooling itself:
// topKAboveFloor and poolChunks run for real over a production-sized batch, so a
// dedupe or truncation change that quietly shrank the pool below the cap would
// fail here and nowhere else.
func TestRetrievalArmsInjectDifferentExcerptVolumes(t *testing.T) {
	// A production-sized batch: HACKERNEWS_HITS_PER_PAGE / EVAL_BATCH_SIZE is 30.
	const batchSize = 30

	// Each one-hot query claims one chunk of its own, so the pool holds ~batchSize
	// distinct chunks. A cap at or above that can never bind here - the fixture
	// runs out of chunks first, which would fail below as if production were
	// broken. Say so plainly instead.
	if RETRIEVAL_POOL_CAP >= batchSize {
		t.Fatalf("fixture limitation, not a production bug: a %d-story one-hot batch yields ~%d distinct chunks, too few for a cap of %d to bind. Widen the fixture (more queries than the cap), then re-run.",
			batchSize, batchSize, RETRIEVAL_POOL_CAP)
	}

	// One chunk per dimension, so a one-hot query scores exactly one chunk at
	// 1.0 and every other at 0.0 - enough distinct chunks that pooling
	// batchSize stories x RETRIEVAL_TOP_K_PER_STORY overflows
	// RETRIEVAL_POOL_CAP, which is the condition the scored eval run was in.
	chunkCount := 2 * batchSize
	chunks := make([]Chunk, 0, chunkCount)
	for i := 0; i < chunkCount; i++ {
		embedding := make([]float32, chunkCount)
		embedding[i] = 1
		chunks = append(chunks, Chunk{
			Source:     "corpus.md",
			ChunkIndex: i,
			Text:       fmt.Sprintf("chunk %d", i),
			Embedding:  embedding,
		})
	}
	index := CorpusIndex{Chunks: chunks}

	oneHot := func(i int) []float32 {
		vec := make([]float32, chunkCount)
		vec[i] = 1
		return vec
	}

	originalLoad, originalQuery, originalQueries := loadCorpusIndex, embedQuery, embedQueries
	defer func() { loadCorpusIndex, embedQuery, embedQueries = originalLoad, originalQuery, originalQueries }()
	loadCorpusIndex = func(path string) (CorpusIndex, error) { return index, nil }

	queries := make([]string, 0, batchSize)
	for i := 0; i < batchSize; i++ {
		queries = append(queries, fmt.Sprintf("story %d", i))
	}

	embedQuery = func(query string) ([]float32, error) { return oneHot(0), nil }
	blended, err := RetrieveContext("blended query", RETRIEVAL_TOP_K)
	if err != nil {
		t.Fatalf("RetrieveContext returned error: %v", err)
	}

	embedQueries = func(qs []string) ([][]float32, error) {
		vecs := make([][]float32, 0, len(qs))
		for i := range qs {
			vecs = append(vecs, oneHot(i))
		}
		return vecs, nil
	}
	pooled, err := RetrievePooledContext(queries)
	if err != nil {
		t.Fatalf("RetrievePooledContext returned error: %v", err)
	}

	if len(blended) != RETRIEVAL_TOP_K {
		t.Errorf("arm 2 supplied %d excerpts, want RETRIEVAL_TOP_K = %d", len(blended), RETRIEVAL_TOP_K)
	}
	if len(pooled) != RETRIEVAL_POOL_CAP {
		t.Errorf("arm 3 supplied %d excerpts, want the cap to bind at RETRIEVAL_POOL_CAP = %d", len(pooled), RETRIEVAL_POOL_CAP)
	}
	if len(blended) == len(pooled) {
		t.Fatalf("the arms now inject the same excerpt count (%d); see this test's doc comment before deleting it", len(blended))
	}
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

func TestBestSimilarityPerQuery(t *testing.T) {
	index := CorpusIndex{Chunks: []Chunk{
		{Text: "east", Embedding: []float32{1, 0}},
		{Text: "north", Embedding: []float32{0, 1}},
	}}

	t.Run("returns each query's best chunk score, in order", func(t *testing.T) {
		originalLoad, originalEmbed := loadCorpusIndex, embedQueries
		defer func() { loadCorpusIndex, embedQueries = originalLoad, originalEmbed }()

		loadCorpusIndex = func(path string) (CorpusIndex, error) { return index, nil }
		embedQueries = func(queries []string) ([][]float32, error) {
			return [][]float32{
				{1, 0},     // exactly "east" -> 1
				{0.6, 0.8}, // 0.6 to east, 0.8 to north -> best 0.8
			}, nil
		}

		got, err := BestSimilarityPerQuery([]string{"a", "b"})
		if err != nil {
			t.Fatalf("BestSimilarityPerQuery returned error: %v", err)
		}
		// Tolerance is float32-sized: the embeddings round-trip through float32,
		// so 0.8 comes back as 0.79999999.
		if len(got) != 2 || math.Abs(got[0]-1) > 1e-6 || math.Abs(got[1]-0.8) > 1e-6 {
			t.Errorf("scores = %v, want [1 0.8]", got)
		}
	})

	t.Run("errors on an empty index rather than reporting -1 scores", func(t *testing.T) {
		originalLoad, originalEmbed := loadCorpusIndex, embedQueries
		defer func() { loadCorpusIndex, embedQueries = originalLoad, originalEmbed }()

		loadCorpusIndex = func(path string) (CorpusIndex, error) { return CorpusIndex{}, nil }
		embedQueries = func(queries []string) ([][]float32, error) {
			t.Fatal("embedQueries should not be called for an empty index")
			return nil, nil
		}

		if _, err := BestSimilarityPerQuery([]string{"a"}); err == nil {
			t.Error("expected an error when the index holds no chunks")
		}
	})

	t.Run("errors when embedding fails", func(t *testing.T) {
		originalLoad, originalEmbed := loadCorpusIndex, embedQueries
		defer func() { loadCorpusIndex, embedQueries = originalLoad, originalEmbed }()

		loadCorpusIndex = func(path string) (CorpusIndex, error) { return index, nil }
		embedQueries = func(queries []string) ([][]float32, error) {
			return nil, errors.New("rate limited")
		}

		if _, err := BestSimilarityPerQuery([]string{"a"}); err == nil {
			t.Error("expected an error when embedding fails")
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
