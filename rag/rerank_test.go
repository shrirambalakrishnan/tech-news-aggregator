package rag

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/shrirambalakrishnan/tech-news/voyageapi"
)

// stubRerankSeams points the three seams RetrieveRerankedContext uses at fakes
// that fail the test if called, and restores them afterwards. Each test then
// overrides only what it needs, so "this path must not touch the network" is
// enforced by default.
func stubRerankSeams(t *testing.T) {
	t.Helper()
	originalLoad, originalEmbed, originalRerank := loadCorpusIndex, embedQueries, rerankQueries

	loadCorpusIndex = func(path string) (CorpusIndex, error) {
		t.Fatalf("unexpected corpus index load")
		return CorpusIndex{}, nil
	}
	embedQueries = func(texts []string) ([][]float32, error) {
		t.Fatalf("unexpected embed call")
		return nil, nil
	}
	rerankQueries = func(queries []string, documents [][]string, topK int) ([][]voyageapi.RerankResult, error) {
		t.Fatalf("unexpected rerank call")
		return nil, nil
	}

	t.Cleanup(func() {
		loadCorpusIndex, embedQueries, rerankQueries = originalLoad, originalEmbed, originalRerank
	})
}

// oneHotIndex builds n chunks whose embeddings are the n unit vectors, so a
// one-hot query scores exactly one chunk at 1.0 and the rest at 0.0. Chunk i's
// text is "chunk i".
func oneHotIndex(n int) CorpusIndex {
	chunks := make([]Chunk, 0, n)
	for i := 0; i < n; i++ {
		embedding := make([]float32, n)
		embedding[i] = 1
		chunks = append(chunks, Chunk{
			Source:     "corpus.md",
			ChunkIndex: i,
			Text:       fmt.Sprintf("chunk %d", i),
			Embedding:  embedding,
		})
	}
	return CorpusIndex{Chunks: chunks}
}

func oneHotVec(i, n int) []float32 {
	vec := make([]float32, n)
	vec[i] = 1
	return vec
}

// TestRetrieveRerankedContextSendsCandidatesPerStory pins the candidate stage:
// each story hands the cross-encoder exactly RERANK_CANDIDATES_PER_STORY chunks
// (the sizing the ~52s pacing and the ~5h eval estimate are computed from), and
// asks it for RERANK_TOP_K_PER_STORY back.
func TestRetrieveRerankedContextSendsCandidatesPerStory(t *testing.T) {
	stubRerankSeams(t)

	const chunkCount = 20
	index := oneHotIndex(chunkCount)
	loadCorpusIndex = func(path string) (CorpusIndex, error) { return index, nil }
	embedQueries = func(texts []string) ([][]float32, error) {
		return [][]float32{oneHotVec(0, chunkCount), oneHotVec(1, chunkCount)}, nil
	}

	var gotQueries []string
	var gotDocuments [][]string
	var gotTopK int
	rerankQueries = func(queries []string, documents [][]string, topK int) ([][]voyageapi.RerankResult, error) {
		gotQueries, gotDocuments, gotTopK = queries, documents, topK
		out := make([][]voyageapi.RerankResult, len(queries))
		for i := range queries {
			out[i] = []voyageapi.RerankResult{{Index: 0, RelevanceScore: 1}}
		}
		return out, nil
	}

	if _, err := RetrieveRerankedContext([]string{"story A", "story B"}); err != nil {
		t.Fatalf("RetrieveRerankedContext returned error: %v", err)
	}

	if want := []string{"story A", "story B"}; !reflect.DeepEqual(gotQueries, want) {
		t.Errorf("queries reaching the reranker = %v, want %v", gotQueries, want)
	}
	if len(gotDocuments) != 2 {
		t.Fatalf("expected 2 document lists, got %d", len(gotDocuments))
	}
	for i, docs := range gotDocuments {
		if len(docs) != RERANK_CANDIDATES_PER_STORY {
			t.Errorf("story %d sent %d candidates, want RERANK_CANDIDATES_PER_STORY (%d) — this count sizes the ~52s pacing per story",
				i, len(docs), RERANK_CANDIDATES_PER_STORY)
		}
	}
	if gotTopK != RERANK_TOP_K_PER_STORY {
		t.Errorf("top_k = %d, want RERANK_TOP_K_PER_STORY (%d)", gotTopK, RERANK_TOP_K_PER_STORY)
	}
}

// TestRerankOrderBeatsCosineOrder is the test that proves the arm does what it
// claims. The candidates are handed over in cosine order; the reranker disagrees
// with that order, and the pool must follow the reranker.
func TestRerankOrderBeatsCosineOrder(t *testing.T) {
	stubRerankSeams(t)

	// Three chunks at descending cosine similarity to the single query.
	index := CorpusIndex{Chunks: []Chunk{
		{Source: "a.md", ChunkIndex: 0, Text: "best by cosine", Embedding: []float32{1, 0}},
		{Source: "b.md", ChunkIndex: 0, Text: "second by cosine", Embedding: []float32{0.8, 0.6}},
		{Source: "c.md", ChunkIndex: 0, Text: "third by cosine", Embedding: []float32{0.6, 0.8}},
	}}
	loadCorpusIndex = func(path string) (CorpusIndex, error) { return index, nil }
	embedQueries = func(texts []string) ([][]float32, error) { return [][]float32{{1, 0}}, nil }

	var gotDocuments [][]string
	rerankQueries = func(queries []string, documents [][]string, topK int) ([][]voyageapi.RerankResult, error) {
		gotDocuments = documents
		// The cross-encoder reverses cosine's verdict: the third candidate wins,
		// the second is runner-up, the cosine leader is not kept at all.
		return [][]voyageapi.RerankResult{{
			{Index: 2, RelevanceScore: 0.95},
			{Index: 1, RelevanceScore: 0.60},
		}}, nil
	}

	got, err := RetrieveRerankedContext([]string{"a title"})
	if err != nil {
		t.Fatalf("RetrieveRerankedContext returned error: %v", err)
	}

	// Candidates go over in cosine order — that is what the returned indices
	// address.
	want := []string{"best by cosine", "second by cosine", "third by cosine"}
	if !reflect.DeepEqual(gotDocuments[0], want) {
		t.Errorf("candidates sent = %v, want cosine order %v", gotDocuments[0], want)
	}

	// ...and the pool comes back in rerank order, not cosine order.
	if wantPool := []string{"third by cosine", "second by cosine"}; !reflect.DeepEqual(got, wantPool) {
		t.Errorf("pool = %v, want rerank order %v (if this returns cosine's order, arm 4 is arm 3)", got, wantPool)
	}
}

// TestRerankResultsMapBackByIndex: Voyage returns results reordered and
// truncated, so the mapping from result to chunk is by Index alone. A slip here
// attaches every score to the wrong chunk and retrieval degrades silently.
func TestRerankResultsMapBackByIndex(t *testing.T) {
	stubRerankSeams(t)

	const chunkCount = 10
	index := oneHotIndex(chunkCount)
	loadCorpusIndex = func(path string) (CorpusIndex, error) { return index, nil }
	// One story whose top cosine match is chunk 3; the rest score 0 and fill the
	// shortlist in index order (chunks 0,1,2,4,5 — stable sort).
	embedQueries = func(texts []string) ([][]float32, error) {
		return [][]float32{oneHotVec(3, chunkCount)}, nil
	}

	var sentDocs []string
	rerankQueries = func(queries []string, documents [][]string, topK int) ([][]voyageapi.RerankResult, error) {
		sentDocs = documents[0]
		// Pick the LAST submitted candidate, out of response order.
		last := len(documents[0]) - 1
		return [][]voyageapi.RerankResult{{{Index: last, RelevanceScore: 0.9}}}, nil
	}

	got, err := RetrieveRerankedContext([]string{"a title"})
	if err != nil {
		t.Fatalf("RetrieveRerankedContext returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 pooled excerpt, got %d", len(got))
	}
	if got[0] != sentDocs[len(sentDocs)-1] {
		t.Errorf("pooled %q, want the candidate at the returned index (%q)", got[0], sentDocs[len(sentDocs)-1])
	}
}

func TestRerankRejectsOutOfRangeResultIndex(t *testing.T) {
	stubRerankSeams(t)

	index := oneHotIndex(8)
	loadCorpusIndex = func(path string) (CorpusIndex, error) { return index, nil }
	embedQueries = func(texts []string) ([][]float32, error) { return [][]float32{oneHotVec(0, 8)}, nil }
	rerankQueries = func(queries []string, documents [][]string, topK int) ([][]voyageapi.RerankResult, error) {
		return [][]voyageapi.RerankResult{{{Index: 99, RelevanceScore: 1}}}, nil
	}

	if _, err := RetrieveRerankedContext([]string{"a title"}); err == nil {
		t.Fatal("expected an error for an out-of-range rerank index, got nil")
	}
}

// TestRerankedPoolIsCappedAndDeduped: pooling is arm 3's, unchanged — the same
// cap bounds the prompt and the same chunk retrieved by several stories survives
// once, at its best score.
func TestRerankedPoolIsCappedAndDeduped(t *testing.T) {
	t.Run("caps at RETRIEVAL_POOL_CAP", func(t *testing.T) {
		stubRerankSeams(t)

		// Enough distinct chunks that 30 stories x RERANK_TOP_K_PER_STORY
		// overflows the cap.
		const batchSize = 30
		chunkCount := 2 * batchSize
		if RETRIEVAL_POOL_CAP >= batchSize*RERANK_TOP_K_PER_STORY {
			t.Fatalf("fixture limitation: a cap of %d cannot bind on %d kept chunks", RETRIEVAL_POOL_CAP, batchSize*RERANK_TOP_K_PER_STORY)
		}

		index := oneHotIndex(chunkCount)
		loadCorpusIndex = func(path string) (CorpusIndex, error) { return index, nil }
		embedQueries = func(texts []string) ([][]float32, error) {
			vecs := make([][]float32, 0, len(texts))
			for i := range texts {
				vecs = append(vecs, oneHotVec(i, chunkCount))
			}
			return vecs, nil
		}
		rerankQueries = func(queries []string, documents [][]string, topK int) ([][]voyageapi.RerankResult, error) {
			out := make([][]voyageapi.RerankResult, len(queries))
			for i := range queries {
				// Each story keeps two DISTINCT candidates: its own one-hot
				// chunk (rank 0) and whichever filler sits at rank 1.
				out[i] = []voyageapi.RerankResult{
					{Index: 0, RelevanceScore: 0.9},
					{Index: 1, RelevanceScore: 0.5},
				}
			}
			return out, nil
		}

		queries := make([]string, batchSize)
		for i := range queries {
			queries[i] = fmt.Sprintf("story %d", i)
		}

		got, err := RetrieveRerankedContext(queries)
		if err != nil {
			t.Fatalf("RetrieveRerankedContext returned error: %v", err)
		}
		if len(got) != RETRIEVAL_POOL_CAP {
			t.Errorf("pooled %d excerpts, want the cap %d", len(got), RETRIEVAL_POOL_CAP)
		}
	})

	t.Run("dedupes a chunk retrieved by several stories", func(t *testing.T) {
		stubRerankSeams(t)

		index := CorpusIndex{Chunks: []Chunk{
			{Source: "shared.md", ChunkIndex: 0, Text: "shared chunk", Embedding: []float32{1, 0}},
			{Source: "other.md", ChunkIndex: 0, Text: "other chunk", Embedding: []float32{0, 1}},
		}}
		loadCorpusIndex = func(path string) (CorpusIndex, error) { return index, nil }
		embedQueries = func(texts []string) ([][]float32, error) {
			return [][]float32{{1, 0}, {1, 0}}, nil
		}
		rerankQueries = func(queries []string, documents [][]string, topK int) ([][]voyageapi.RerankResult, error) {
			// Both stories keep the same chunk, at different scores.
			return [][]voyageapi.RerankResult{
				{{Index: 0, RelevanceScore: 0.4}},
				{{Index: 0, RelevanceScore: 0.8}},
			}, nil
		}

		got, err := RetrieveRerankedContext([]string{"a", "b"})
		if err != nil {
			t.Fatalf("RetrieveRerankedContext returned error: %v", err)
		}
		if len(got) != 1 || got[0] != "shared chunk" {
			t.Errorf("pool = %v, want the shared chunk exactly once", got)
		}
	})
}

// TestRerankSkipsStoriesWithNoCandidates: an empty document list is a request
// the reranker cannot answer, and at ~52s of pacing per call, sending one costs
// real wall clock for nothing.
func TestRerankSkipsStoriesWithNoCandidates(t *testing.T) {
	stubRerankSeams(t)

	floor := RERANK_CANDIDATE_FLOOR
	RERANK_CANDIDATE_FLOOR = 0.5 // make the floor bite, which it cannot at 0.0
	defer func() { RERANK_CANDIDATE_FLOOR = floor }()

	index := CorpusIndex{Chunks: []Chunk{
		{Source: "a.md", ChunkIndex: 0, Text: "match", Embedding: []float32{1, 0}},
	}}
	loadCorpusIndex = func(path string) (CorpusIndex, error) { return index, nil }
	embedQueries = func(texts []string) ([][]float32, error) {
		// Story 0 matches; story 1 is orthogonal, so it clears no floor.
		return [][]float32{{1, 0}, {0, 1}}, nil
	}

	var gotQueries []string
	var gotDocuments [][]string
	rerankQueries = func(queries []string, documents [][]string, topK int) ([][]voyageapi.RerankResult, error) {
		gotQueries, gotDocuments = queries, documents
		return [][]voyageapi.RerankResult{{{Index: 0, RelevanceScore: 1}}}, nil
	}

	if _, err := RetrieveRerankedContext([]string{"matching story", "unrelated story"}); err != nil {
		t.Fatalf("RetrieveRerankedContext returned error: %v", err)
	}

	if want := []string{"matching story"}; !reflect.DeepEqual(gotQueries, want) {
		t.Errorf("queries reranked = %v, want only the story with candidates %v", gotQueries, want)
	}
	for i, docs := range gotDocuments {
		if len(docs) == 0 {
			t.Errorf("document list %d is empty; an empty rerank request must never be sent", i)
		}
	}
}

// TestRerankCandidateFloorIsInert documents, as an executable claim, what the
// constant's comment says: candidates are selected by RANK, so at the shipped
// 0.0 the floor cannot change what is sent. If this ever fails, the floor became
// live and the arm's recorded numbers are no longer floor-free.
func TestRerankCandidateFloorIsInert(t *testing.T) {
	if RERANK_CANDIDATE_FLOOR != 0.0 {
		t.Fatalf("RERANK_CANDIDATE_FLOOR = %v, want 0.0 — arm 4's recorded numbers were measured with the candidate floor never firing; re-run eval 4 before changing it", RERANK_CANDIDATE_FLOOR)
	}

	// A cosine below 0.0 requires vectors pointing apart, which no chunk does
	// against a query drawn from the same corpus; so at 0.0, rank governs.
	chunks := []Chunk{
		{Source: "a.md", ChunkIndex: 0, Text: "aligned", Embedding: []float32{1, 0}},
		{Source: "b.md", ChunkIndex: 0, Text: "orthogonal", Embedding: []float32{0, 1}},
	}
	got := topKAboveFloor(chunks, []float32{1, 0}, RERANK_CANDIDATES_PER_STORY, RERANK_CANDIDATE_FLOOR)
	if len(got) != 2 {
		t.Errorf("a 0.0 floor kept %d of 2 chunks; it is meant to filter nothing", len(got))
	}
}

// TestRerankRetrievalErrorsNeverDegrade: every failure is an error, never a
// thinner-but-usable context. Silently returning fewer excerpts would publish
// numbers labelled arm 4 that were measured against something else.
func TestRerankRetrievalErrorsNeverDegrade(t *testing.T) {
	t.Run("missing index", func(t *testing.T) {
		stubRerankSeams(t)
		loadCorpusIndex = func(path string) (CorpusIndex, error) {
			return CorpusIndex{}, errors.New("no such file")
		}

		if _, err := RetrieveRerankedContext([]string{"a"}); err == nil {
			t.Fatal("expected an error when the corpus index is missing, got nil")
		}
	})

	t.Run("embed failure", func(t *testing.T) {
		stubRerankSeams(t)
		loadCorpusIndex = func(path string) (CorpusIndex, error) { return oneHotIndex(4), nil }
		embedQueries = func(texts []string) ([][]float32, error) { return nil, errors.New("voyage down") }

		if _, err := RetrieveRerankedContext([]string{"a"}); err == nil {
			t.Fatal("expected an error when embedding fails, got nil")
		}
	})

	t.Run("embed returns the wrong number of vectors", func(t *testing.T) {
		stubRerankSeams(t)
		loadCorpusIndex = func(path string) (CorpusIndex, error) { return oneHotIndex(4), nil }
		embedQueries = func(texts []string) ([][]float32, error) {
			return [][]float32{oneHotVec(0, 4)}, nil // one vector for two queries
		}

		_, err := RetrieveRerankedContext([]string{"a", "b"})
		if err == nil {
			t.Fatal("expected an error on an embed/query length mismatch, got nil")
		}
		if !strings.Contains(err.Error(), "expected 2") {
			t.Errorf("error should name the mismatch, got %q", err)
		}
	})

	t.Run("rerank failure", func(t *testing.T) {
		stubRerankSeams(t)
		loadCorpusIndex = func(path string) (CorpusIndex, error) { return oneHotIndex(4), nil }
		embedQueries = func(texts []string) ([][]float32, error) { return [][]float32{oneHotVec(0, 4)}, nil }
		rerankQueries = func(queries []string, documents [][]string, topK int) ([][]voyageapi.RerankResult, error) {
			return nil, errors.New("rate limited for good")
		}

		if _, err := RetrieveRerankedContext([]string{"a"}); err == nil {
			t.Fatal("expected an error when reranking fails, got nil")
		}
	})

	t.Run("rerank returns the wrong number of result lists", func(t *testing.T) {
		stubRerankSeams(t)
		loadCorpusIndex = func(path string) (CorpusIndex, error) { return oneHotIndex(4), nil }
		embedQueries = func(texts []string) ([][]float32, error) {
			return [][]float32{oneHotVec(0, 4), oneHotVec(1, 4)}, nil
		}
		rerankQueries = func(queries []string, documents [][]string, topK int) ([][]voyageapi.RerankResult, error) {
			return [][]voyageapi.RerankResult{{{Index: 0, RelevanceScore: 1}}}, nil // one list for two stories
		}

		if _, err := RetrieveRerankedContext([]string{"a", "b"}); err == nil {
			t.Fatal("expected an error when the reranker returns the wrong number of lists, got nil")
		}
	})
}

// TestRerankLeavesArm3ConstantsAlone: arm 3's recorded numbers stand only if arm
// 4 shares none of its knobs. RERANK_CANDIDATE_FLOOR exists precisely so the two
// floors can differ in intent.
func TestRerankLeavesArm3ConstantsAlone(t *testing.T) {
	if RETRIEVAL_SIMILARITY_FLOOR != 0.3330 {
		t.Errorf("RETRIEVAL_SIMILARITY_FLOOR = %v, want arm 3's measured 0.3330 left untouched", RETRIEVAL_SIMILARITY_FLOOR)
	}
	if RERANK_TOP_K_PER_STORY != RETRIEVAL_TOP_K_PER_STORY {
		t.Errorf("RERANK_TOP_K_PER_STORY (%d) != RETRIEVAL_TOP_K_PER_STORY (%d); they are equal on purpose so arms 3 and 4 pool comparable excerpt volumes",
			RERANK_TOP_K_PER_STORY, RETRIEVAL_TOP_K_PER_STORY)
	}
	if RERANK_CANDIDATES_PER_STORY < RERANK_TOP_K_PER_STORY {
		t.Errorf("RERANK_CANDIDATES_PER_STORY (%d) < RERANK_TOP_K_PER_STORY (%d): nothing to rerank",
			RERANK_CANDIDATES_PER_STORY, RERANK_TOP_K_PER_STORY)
	}
}
