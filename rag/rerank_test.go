package rag

import (
	"bytes"
	"errors"
	"log"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/shrirambalakrishnan/tech-news/voyageapi"
)

// rerankFake is the shape of the rerankQueries DI seam.
type rerankFake func(queries []string, documents [][]string) ([][]voyageapi.RerankResult, error)

// stubRerankStage swaps all three seams arm 4's retrieval touches, so no test
// reaches the disk or the network.
func stubRerankStage(t *testing.T, index CorpusIndex, embed func([]string) ([][]float32, error), rerank rerankFake) {
	t.Helper()
	originalLoad, originalEmbed, originalRerank := loadCorpusIndex, embedQueries, rerankQueries
	loadCorpusIndex = func(path string) (CorpusIndex, error) { return index, nil }
	embedQueries = embed
	rerankQueries = rerank
	t.Cleanup(func() {
		loadCorpusIndex, embedQueries, rerankQueries = originalLoad, originalEmbed, originalRerank
	})
}

// fourChunkIndex spreads four chunks around the unit circle so a query vector
// can be aimed to produce a known cosine ordering. All four sit in the positive
// quadrant, mirroring the cone real embeddings occupy — the observed range over
// the actual corpus is ~0.09-0.51, never negative.
//
// Against a query of {1, 0} the cosine order is: east (1), north-east (0.8),
// faint (0.05), north (0).
func fourChunkIndex() CorpusIndex {
	return CorpusIndex{Chunks: []Chunk{
		{Source: "a.md", ChunkIndex: 0, Text: "east", Embedding: []float32{1, 0}},
		{Source: "b.md", ChunkIndex: 0, Text: "north-east", Embedding: []float32{0.8, 0.6}},
		{Source: "c.md", ChunkIndex: 0, Text: "north", Embedding: []float32{0, 1}},
		{Source: "d.md", ChunkIndex: 0, Text: "faint", Embedding: []float32{0.05, 0.9987}},
	}}
}

// TestRerankOrderBeatsCosineOrder is the test that proves the arm does the thing
// it claims: candidates arrive in cosine order and leave in rerank order. If
// rerank scores were ignored the pool would still be non-empty and the run would
// still complete — this is the only thing that catches that.
func TestRerankOrderBeatsCosineOrder(t *testing.T) {
	var submitted [][]string
	stubRerankStage(t, fourChunkIndex(),
		func(queries []string) ([][]float32, error) {
			return [][]float32{{1, 0}}, nil // cosine order: east, north-east, north, west
		},
		func(queries []string, documents [][]string) ([][]voyageapi.RerankResult, error) {
			submitted = documents
			// The cross-encoder disagrees with cosine: candidate 3 ("north"),
			// which cosine ranked LAST, beats candidate 0 ("east"), which
			// cosine ranked first.
			return [][]voyageapi.RerankResult{{
				{Index: 3, RelevanceScore: 0.95},
				{Index: 0, RelevanceScore: 0.10},
			}}, nil
		})

	texts, err := RetrieveRerankedContext([]string{"a story"})
	if err != nil {
		t.Fatalf("RetrieveRerankedContext returned error: %v", err)
	}
	if !reflect.DeepEqual(texts, []string{"north", "east"}) {
		t.Fatalf("texts = %v, want [north east] — the pool did not follow the rerank score", texts)
	}
	// Sanity: the candidates were submitted in cosine order, so the rerank
	// really did reorder rather than being handed the answer.
	if len(submitted) != 1 || submitted[0][0] != "east" || submitted[0][3] != "north" {
		t.Fatalf("candidates were submitted as %v, want cosine order east...north", submitted)
	}
}

// TestRerankSendsCandidatesPerStoryAtTheCandidateFloor pins the request shape
// the ~52s pacing was sized for: RERANK_CANDIDATES_PER_STORY documents per
// story, one query per story.
func TestRerankSendsCandidatesPerStoryAtTheCandidateFloor(t *testing.T) {
	var gotQueries []string
	var gotDocuments [][]string

	stubRerankStage(t, fourChunkIndex(),
		func(queries []string) ([][]float32, error) {
			return [][]float32{{1, 0}, {0, 1}}, nil
		},
		func(queries []string, documents [][]string) ([][]voyageapi.RerankResult, error) {
			gotQueries, gotDocuments = queries, documents
			results := make([][]voyageapi.RerankResult, len(queries))
			for i := range queries {
				results[i] = []voyageapi.RerankResult{{Index: 0, RelevanceScore: 0.5}}
			}
			return results, nil
		})

	stories := []string{"story one", "story two"}
	if _, err := RetrieveRerankedContext(stories); err != nil {
		t.Fatalf("RetrieveRerankedContext returned error: %v", err)
	}

	if !reflect.DeepEqual(gotQueries, stories) {
		t.Errorf("queries reranked = %v, want one per story %v", gotQueries, stories)
	}
	// The index holds 4 chunks and RERANK_CANDIDATES_PER_STORY is 6, so every
	// chunk is a candidate — which is exactly what the inert floor implies.
	for i, docs := range gotDocuments {
		if len(docs) != 4 {
			t.Errorf("story %d submitted %d candidates, want all 4 chunks (the candidate floor must not filter)", i, len(docs))
		}
	}
}

// TestRerankCandidateFloorIsInert makes the documented inertness executable.
//
// Inert IN PRACTICE, not in principle: topKAboveFloor drops scores strictly
// below the floor, so 0.0 would filter a negative cosine — there just aren't
// any, since embeddings sit in a positive cone (observed range ~0.09-0.51). What
// this asserts is the consequence that matters: a chunk scoring far below arm
// 3's 0.3330 relevance floor still reaches the cross-encoder, because candidates
// are chosen by RANK and not by score. That is the recall posture arm 4's
// candidate stage is supposed to have.
func TestRerankCandidateFloorIsInert(t *testing.T) {
	if RERANK_CANDIDATE_FLOOR != 0.0 {
		t.Fatalf("RERANK_CANDIDATE_FLOOR = %v, want 0.0 — update the inertness argument if this is deliberate", RERANK_CANDIDATE_FLOOR)
	}

	var gotDocuments [][]string
	stubRerankStage(t, fourChunkIndex(),
		func(queries []string) ([][]float32, error) { return [][]float32{{1, 0}}, nil },
		func(queries []string, documents [][]string) ([][]voyageapi.RerankResult, error) {
			gotDocuments = documents
			return [][]voyageapi.RerankResult{{{Index: 0, RelevanceScore: 0.5}}}, nil
		})

	if _, err := RetrieveRerankedContext([]string{"a story"}); err != nil {
		t.Fatalf("RetrieveRerankedContext returned error: %v", err)
	}

	// "faint" scores 0.05 and "north" scores exactly 0.0 against this query.
	// Arm 3's floor would drop both; this stage must not.
	for _, want := range []string{"faint", "north"} {
		found := false
		for _, doc := range gotDocuments[0] {
			if doc == want {
				found = true
			}
		}
		if !found {
			t.Errorf("weakly-scoring chunk %q was dropped: the candidate floor is filtering, not inert", want)
		}
	}
}

// TestRerankKeepsOnlyTopKPerStory: no top_k is sent, so the response carries a
// score for every candidate and this local cut is the ONLY thing enforcing "keep
// 2 per story". Drop it and each story would contribute 6, the pool would still
// cap at 20, and the run would complete and score normally with the
// volume-matched property silently gone.
func TestRerankKeepsOnlyTopKPerStory(t *testing.T) {
	original := RERANK_TOP_K_PER_STORY
	RERANK_TOP_K_PER_STORY = 2
	t.Cleanup(func() { RERANK_TOP_K_PER_STORY = original })

	stubRerankStage(t, fourChunkIndex(),
		func(queries []string) ([][]float32, error) { return [][]float32{{1, 0}}, nil },
		func(queries []string, documents [][]string) ([][]voyageapi.RerankResult, error) {
			// Every submitted candidate comes back scored, best first.
			return [][]voyageapi.RerankResult{{
				{Index: 0, RelevanceScore: 0.9},
				{Index: 1, RelevanceScore: 0.8},
				{Index: 2, RelevanceScore: 0.7},
				{Index: 3, RelevanceScore: 0.6},
			}}, nil
		})

	texts, err := RetrieveRerankedContext([]string{"a story"})
	if err != nil {
		t.Fatalf("RetrieveRerankedContext returned error: %v", err)
	}
	if len(texts) != 2 {
		t.Fatalf("pooled %d excerpts from one story, want %d — the local cut did not run", len(texts), RERANK_TOP_K_PER_STORY)
	}
	// The cut must keep the BEST two, not the first two off the wire in some
	// other order - the response is descending by score.
	if !reflect.DeepEqual(texts, []string{"east", "north-east"}) {
		t.Fatalf("texts = %v, want the two highest-scoring candidates", texts)
	}
}

// TestRerankLogsEveryScoredCandidateNotJustTheKept is why no top_k is sent. The
// run log is the score dump a rerank floor gets calibrated from, and a floor is
// a decision about where to cut — so a dump censored at the current cut cannot
// inform one. The discarded scores cost nothing extra: the cross-encoder scored
// them either way.
func TestRerankLogsEveryScoredCandidateNotJustTheKept(t *testing.T) {
	original := RERANK_TOP_K_PER_STORY
	RERANK_TOP_K_PER_STORY = 2
	t.Cleanup(func() { RERANK_TOP_K_PER_STORY = original })

	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	stubRerankStage(t, fourChunkIndex(),
		func(queries []string) ([][]float32, error) { return [][]float32{{1, 0}}, nil },
		func(queries []string, documents [][]string) ([][]voyageapi.RerankResult, error) {
			return [][]voyageapi.RerankResult{{
				{Index: 0, RelevanceScore: 0.90},
				{Index: 1, RelevanceScore: 0.80},
				{Index: 2, RelevanceScore: 0.70},
				{Index: 3, RelevanceScore: 0.60},
			}}, nil
		})

	if _, err := RetrieveRerankedContext([]string{"a story"}); err != nil {
		t.Fatalf("RetrieveRerankedContext returned error: %v", err)
	}

	var scoreLines []string
	for _, line := range strings.Split(logged.String(), "\n") {
		if strings.Contains(line, "rerank-score,") {
			scoreLines = append(scoreLines, line)
		}
	}
	if len(scoreLines) != 4 {
		t.Fatalf("logged %d score lines, want 4 (one per scored candidate, not per kept chunk):\n%s",
			len(scoreLines), strings.Join(scoreLines, "\n"))
	}

	// The kept flag is what makes the dump readable: without it a consumer
	// cannot tell which side of the cut a score fell on.
	for i, line := range scoreLines {
		wantKept := i < RERANK_TOP_K_PER_STORY
		if got := strings.HasSuffix(line, ",true"); got != wantKept {
			t.Errorf("score line %d kept=%v, want %v: %s", i, got, wantKept, line)
		}
	}
	// Both scores travel together - a rerank floor is only interesting against
	// the cosine ranking it replaces.
	if !strings.Contains(scoreLines[0], "1.000000,0.900000") {
		t.Errorf("first score line carries %q, want both the cosine and rerank scores", scoreLines[0])
	}
}

// TestRerankPoolsDedupedAndCapped: pooling is arm 3's, unchanged — a chunk
// retrieved by several stories survives once, at its best score, and the pool is
// bounded by RETRIEVAL_POOL_CAP regardless of the batch size.
func TestRerankPoolsDedupedAndCapped(t *testing.T) {
	originalCap := RETRIEVAL_POOL_CAP
	RETRIEVAL_POOL_CAP = 2
	t.Cleanup(func() { RETRIEVAL_POOL_CAP = originalCap })

	stubRerankStage(t, fourChunkIndex(),
		func(queries []string) ([][]float32, error) {
			return [][]float32{{1, 0}, {1, 0}, {0, 1}}, nil
		},
		func(queries []string, documents [][]string) ([][]voyageapi.RerankResult, error) {
			// Stories one and two keep candidate 0 ("east", their cosine
			// leader) and story two scores it highest; story three, which
			// points north, keeps its own candidates 0 ("north") and 1
			// ("faint").
			return [][]voyageapi.RerankResult{
				{{Index: 0, RelevanceScore: 0.30}},
				{{Index: 0, RelevanceScore: 0.99}},
				{{Index: 0, RelevanceScore: 0.20}, {Index: 1, RelevanceScore: 0.50}},
			}, nil
		})

	texts, err := RetrieveRerankedContext([]string{"one", "two", "three"})
	if err != nil {
		t.Fatalf("RetrieveRerankedContext returned error: %v", err)
	}
	if len(texts) != 2 {
		t.Fatalf("pooled %d excerpts, want %d (the cap)", len(texts), RETRIEVAL_POOL_CAP)
	}
	// "east" was retrieved three times but appears once, ranked by its best
	// score (0.99) — CombMAX, exactly as arm 3 pools.
	if texts[0] != "east" {
		t.Fatalf("pool leads with %q, want east at its best score", texts[0])
	}
	if texts[0] == texts[1] {
		t.Fatalf("pool contains %q twice: dedupe did not run", texts[0])
	}
}

// TestRerankSkipsStoriesWithNoCandidates: an empty document list is a request
// the reranker cannot answer, and sending one would pay ~52s of pacing for
// nothing. The stories that DO have candidates must still line up with their
// results after the skip.
func TestRerankSkipsStoriesWithNoCandidates(t *testing.T) {
	originalFloor := RERANK_CANDIDATE_FLOOR
	RERANK_CANDIDATE_FLOOR = 0.9 // force story two's shortlist empty
	t.Cleanup(func() { RERANK_CANDIDATE_FLOOR = originalFloor })

	var gotQueries []string
	var gotDocuments [][]string
	stubRerankStage(t, fourChunkIndex(),
		func(queries []string) ([][]float32, error) {
			// story one points east (cosine 1 on "east"); story two points
			// south-west and clears nothing.
			return [][]float32{{1, 0}, {-0.6, -0.8}}, nil
		},
		func(queries []string, documents [][]string) ([][]voyageapi.RerankResult, error) {
			gotQueries, gotDocuments = queries, documents
			return [][]voyageapi.RerankResult{{{Index: 0, RelevanceScore: 0.7}}}, nil
		})

	texts, err := RetrieveRerankedContext([]string{"story one", "story two"})
	if err != nil {
		t.Fatalf("RetrieveRerankedContext returned error: %v", err)
	}
	if !reflect.DeepEqual(gotQueries, []string{"story one"}) {
		t.Fatalf("queries sent to the reranker = %v, want only the story with candidates", gotQueries)
	}
	for i, docs := range gotDocuments {
		if len(docs) == 0 {
			t.Fatalf("submitted an empty document list at position %d", i)
		}
	}
	if !reflect.DeepEqual(texts, []string{"east"}) {
		t.Fatalf("texts = %v, want [east] — results misaligned after the skip", texts)
	}
}

// TestRerankReturnsNothingWhenNoStoryHasCandidates: with every shortlist empty
// there is nothing to rerank, so the honest answer is no excerpts — not an empty
// request.
func TestRerankReturnsNothingWhenNoStoryHasCandidates(t *testing.T) {
	stubRerankStage(t, CorpusIndex{},
		func(queries []string) ([][]float32, error) { return [][]float32{{1, 0}}, nil },
		func(queries []string, documents [][]string) ([][]voyageapi.RerankResult, error) {
			t.Fatal("the reranker was called with no candidates")
			return nil, nil
		})

	texts, err := RetrieveRerankedContext([]string{"a story"})
	if err != nil {
		t.Fatalf("RetrieveRerankedContext returned error: %v", err)
	}
	if len(texts) != 0 {
		t.Fatalf("texts = %v, want none", texts)
	}
}

// TestRerankRejectsOutOfRangeResultIndex: the seam above is swappable, so this
// is where an out-of-range index would panic while subscripting the candidate
// slice. It has to be an error, not a crash mid-eval.
func TestRerankRejectsOutOfRangeResultIndex(t *testing.T) {
	for _, index := range []int{-1, 99} {
		stubRerankStage(t, fourChunkIndex(),
			func(queries []string) ([][]float32, error) { return [][]float32{{1, 0}}, nil },
			func(queries []string, documents [][]string) ([][]voyageapi.RerankResult, error) {
				return [][]voyageapi.RerankResult{{{Index: index, RelevanceScore: 0.5}}}, nil
			})

		if _, err := RetrieveRerankedContext([]string{"a story"}); err == nil {
			t.Fatalf("rerank index %d: expected an error, got nil", index)
		}
	}
}

// TestRerankFailuresAreErrorsNeverDegradedContext: issue #11's no-fail-soft rule.
// An arm asked for must run as that arm or not at all — silently returning fewer
// excerpts would publish numbers labelled "arm 4" measured under something else.
func TestRerankFailuresAreErrorsNeverDegradedContext(t *testing.T) {
	t.Run("missing index, without embedding", func(t *testing.T) {
		originalLoad, originalEmbed := loadCorpusIndex, embedQueries
		t.Cleanup(func() { loadCorpusIndex, embedQueries = originalLoad, originalEmbed })

		loadCorpusIndex = func(path string) (CorpusIndex, error) {
			return CorpusIndex{}, errors.New("no such file")
		}
		embedQueries = func(queries []string) ([][]float32, error) {
			t.Fatal("embedQueries should not be called when the index is missing")
			return nil, nil
		}

		if _, err := RetrieveRerankedContext([]string{"q"}); err == nil {
			t.Fatal("expected an error when the index is missing")
		}
	})

	t.Run("embedding failure", func(t *testing.T) {
		stubRerankStage(t, fourChunkIndex(),
			func(queries []string) ([][]float32, error) { return nil, errors.New("voyage down") },
			func(queries []string, documents [][]string) ([][]voyageapi.RerankResult, error) {
				t.Fatal("the reranker was called after the embed step failed")
				return nil, nil
			})

		if _, err := RetrieveRerankedContext([]string{"q"}); err == nil {
			t.Fatal("expected an error when embedding fails")
		}
	})

	t.Run("embedded query count mismatch", func(t *testing.T) {
		stubRerankStage(t, fourChunkIndex(),
			func(queries []string) ([][]float32, error) { return [][]float32{{1, 0}}, nil },
			func(queries []string, documents [][]string) ([][]voyageapi.RerankResult, error) {
				t.Fatal("the reranker was called on a short embedding result")
				return nil, nil
			})

		if _, err := RetrieveRerankedContext([]string{"one", "two"}); err == nil {
			t.Fatal("expected an error when the embedder returns fewer vectors than queries")
		}
	})

	t.Run("rerank failure", func(t *testing.T) {
		stubRerankStage(t, fourChunkIndex(),
			func(queries []string) ([][]float32, error) { return [][]float32{{1, 0}}, nil },
			func(queries []string, documents [][]string) ([][]voyageapi.RerankResult, error) {
				return nil, errors.New("rate limited into the ground")
			})

		if _, err := RetrieveRerankedContext([]string{"q"}); err == nil {
			t.Fatal("expected an error when the reranker fails")
		}
	})

	t.Run("rerank result count mismatch", func(t *testing.T) {
		stubRerankStage(t, fourChunkIndex(),
			func(queries []string) ([][]float32, error) { return [][]float32{{1, 0}, {0, 1}}, nil },
			func(queries []string, documents [][]string) ([][]voyageapi.RerankResult, error) {
				return [][]voyageapi.RerankResult{{{Index: 0, RelevanceScore: 0.5}}}, nil
			})

		if _, err := RetrieveRerankedContext([]string{"one", "two"}); err == nil {
			t.Fatal("expected an error when the reranker returns fewer result lists than stories")
		}
	})
}

// TestRerankLeavesArm3ConstantsAlone: arm 3's recorded numbers only stand if arm
// 4 shipped without touching its knobs. Arm 4's own knobs are asserted at their
// planned values, since the pacing and the volume-match argument are computed
// from them.
func TestRerankLeavesArm3ConstantsAlone(t *testing.T) {
	if RETRIEVAL_TOP_K_PER_STORY != 2 {
		t.Errorf("RETRIEVAL_TOP_K_PER_STORY = %d, want 2 (arm 3's value must not move)", RETRIEVAL_TOP_K_PER_STORY)
	}
	if RETRIEVAL_SIMILARITY_FLOOR != 0.3330 {
		t.Errorf("RETRIEVAL_SIMILARITY_FLOOR = %v, want 0.3330 (arm 3's measured floor must not move)", RETRIEVAL_SIMILARITY_FLOOR)
	}
	if RETRIEVAL_POOL_CAP != 20 {
		t.Errorf("RETRIEVAL_POOL_CAP = %d, want 20 (shared with arm 3)", RETRIEVAL_POOL_CAP)
	}
	if RERANK_CANDIDATES_PER_STORY != 6 {
		t.Errorf("RERANK_CANDIDATES_PER_STORY = %d, want 6 — the shape the pacing was measured at", RERANK_CANDIDATES_PER_STORY)
	}
	// Equal on purpose: it is what keeps arm 4's pooled volume comparable to
	// arm 3's, so a delta is about which chunks were chosen, not how many.
	if RERANK_TOP_K_PER_STORY != RETRIEVAL_TOP_K_PER_STORY {
		t.Errorf("RERANK_TOP_K_PER_STORY (%d) != RETRIEVAL_TOP_K_PER_STORY (%d): arm 4 is no longer volume-matched to arm 3",
			RERANK_TOP_K_PER_STORY, RETRIEVAL_TOP_K_PER_STORY)
	}
}
