package armcontext

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/shrirambalakrishnan/tech-news/hackernews_classifier"
	"github.com/shrirambalakrishnan/tech-news/profile"
	"github.com/shrirambalakrishnan/tech-news/rag"
)

// stubLoaders points every DI seam at fakes that fail the test if called, and
// restores them afterwards. Each subtest then overrides only the seam its arm is
// allowed to touch - so "arm 0 must not load anything" is enforced by default
// rather than asserted case by case.
func stubLoaders(t *testing.T) {
	t.Helper()

	loadUserContext = func(path string) (profile.UserContext, error) {
		t.Fatalf("this arm must not load the distilled user context")
		return profile.UserContext{}, nil
	}
	loadRagContext = func(query string, k int) ([]string, error) {
		t.Fatalf("this arm must not retrieve corpus context")
		return nil, nil
	}
	loadPooledRagContext = func(queries []string) ([]string, error) {
		t.Fatalf("this arm must not retrieve pooled corpus context")
		return nil, nil
	}

	t.Cleanup(func() {
		loadUserContext = profile.LoadUserContext
		loadRagContext = rag.RetrieveContext
		loadPooledRagContext = rag.RetrievePooledContext
	})
}

var testStories = []hackernews_classifier.StoryDetail{
	{Id: 1, Title: "story1"},
	{Id: 2, Title: "story2"},
}

func TestBuildProfileArmGeneric(t *testing.T) {
	stubLoaders(t)

	// No override: the static ruleset needs no context at all, so any load here
	// trips the stubs.
	p, err := BuildProfile(hackernews_classifier.ArmGeneric, testStories)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !p.IsEmpty() {
		t.Fatalf("expected empty profile, got %+v", p)
	}
}

func TestBuildProfileArmInterests(t *testing.T) {
	t.Run("maps the loaded context onto the profile", func(t *testing.T) {
		stubLoaders(t)
		loadUserContext = func(path string) (profile.UserContext, error) {
			if path != profile.USER_CONTEXT_FILE {
				t.Errorf("loaded %q, want %q", path, profile.USER_CONTEXT_FILE)
			}
			return profile.UserContext{Summary: "backend work", Interests: []string{"Go", "Spanner"}}, nil
		}

		p, err := BuildProfile(hackernews_classifier.ArmInterests, testStories)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.Summary != "backend work" || len(p.Interests) != 2 {
			t.Fatalf("expected the loaded context mapped into the profile, got %+v", p)
		}
		if len(p.RetrievedExcerpts) != 0 {
			t.Fatalf("arm 1 must carry no RAG excerpts, got %+v", p.RetrievedExcerpts)
		}
	})

	// Failing soft here would classify under arm 0's static rules while
	// reporting arm 1 - the failure issue #11 exists to prevent.
	t.Run("errors when the distilled profile is missing", func(t *testing.T) {
		stubLoaders(t)
		loadUserContext = func(path string) (profile.UserContext, error) {
			return profile.UserContext{}, errors.New("file not found")
		}

		_, err := BuildProfile(hackernews_classifier.ArmInterests, testStories)
		if err == nil {
			t.Fatal("expected an error when the distilled profile is missing, got nil")
		}
		if !strings.Contains(err.Error(), "prebuild") {
			t.Errorf("error should point at the fix (`go run . prebuild`), got %q", err)
		}
	})
}

func TestBuildProfileArmRAG(t *testing.T) {
	t.Run("retrieves with the batch titles as query", func(t *testing.T) {
		stubLoaders(t)
		var gotQuery string
		var gotK int
		loadRagContext = func(query string, k int) ([]string, error) {
			gotQuery, gotK = query, k
			return []string{"chunk about spanner", "chunk about raft"}, nil
		}

		p, err := BuildProfile(hackernews_classifier.ArmRAG, testStories)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// The query shape is the contract production and the eval share; if it
		// drifts, the eval stops measuring what ships.
		if gotQuery != "story1\nstory2" {
			t.Errorf("retrieval query = %q, want the batch titles newline-joined", gotQuery)
		}
		if gotK != rag.RETRIEVAL_TOP_K {
			t.Errorf("k = %d, want RETRIEVAL_TOP_K (%d)", gotK, rag.RETRIEVAL_TOP_K)
		}
		if len(p.RetrievedExcerpts) != 2 || p.RetrievedExcerpts[0] != "chunk about spanner" {
			t.Errorf("expected the retrieved chunks on the profile, got %+v", p.RetrievedExcerpts)
		}
		if p.Summary != "" || len(p.Interests) != 0 {
			t.Errorf("arm 2 must carry no distilled context, got %+v", p)
		}
	})

	t.Run("errors when retrieval fails", func(t *testing.T) {
		stubLoaders(t)
		loadRagContext = func(query string, k int) ([]string, error) {
			return nil, errors.New("corpus index unavailable")
		}

		_, err := BuildProfile(hackernews_classifier.ArmRAG, testStories)
		if err == nil {
			t.Fatal("expected an error when retrieval fails, got nil")
		}
		if !strings.Contains(err.Error(), "embed") {
			t.Errorf("error should point at the fix (`go run . embed`), got %q", err)
		}
	})
}

func TestBuildProfileArmRAGPerStory(t *testing.T) {
	t.Run("retrieves one query per story", func(t *testing.T) {
		stubLoaders(t)
		var gotQueries []string
		loadPooledRagContext = func(queries []string) ([]string, error) {
			gotQueries = queries
			return []string{"chunk about raft", "chunk about spanner"}, nil
		}

		p, err := BuildProfile(hackernews_classifier.ArmRAGPerStory, testStories)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// The whole difference between arms 2 and 3: separate queries, not one
		// blended string. If these ever collapse into one, arm 3 silently
		// becomes arm 2 and the comparison measures nothing.
		want := []string{"story1", "story2"}
		if !reflect.DeepEqual(gotQueries, want) {
			t.Errorf("retrieval queries = %v, want one per story %v", gotQueries, want)
		}
		if len(p.RetrievedExcerpts) != 2 || p.RetrievedExcerpts[0] != "chunk about raft" {
			t.Errorf("expected the pooled chunks on the profile, got %+v", p.RetrievedExcerpts)
		}
		if p.Summary != "" || len(p.Interests) != 0 {
			t.Errorf("arm 3 must carry no distilled context, got %+v", p)
		}
	})

	t.Run("errors when retrieval fails", func(t *testing.T) {
		stubLoaders(t)
		loadPooledRagContext = func(queries []string) ([]string, error) {
			return nil, errors.New("corpus index unavailable")
		}

		_, err := BuildProfile(hackernews_classifier.ArmRAGPerStory, testStories)
		if err == nil {
			t.Fatal("expected an error when retrieval fails, got nil")
		}
		if !strings.Contains(err.Error(), "embed") {
			t.Errorf("error should point at the fix (`go run . embed`), got %q", err)
		}
	})
}

// The two retrieval arms were designed to differ in excerpt SELECTION only, but
// they also differ in excerpt VOLUME - arm 2 asks for rag.RETRIEVAL_TOP_K (5),
// arm 3 is bounded by rag.RETRIEVAL_POOL_CAP (20). Since excerpts are inlined
// verbatim into the prompt, that makes arm 3's prompt several times larger, and
// prompt size is its own plausible cause of an eval delta. README and CLAUDE.md
// document the confound; this pins it so the code cannot drift away from them.
//
// This is the half of the invariant that lives HERE: arm 2's k is chosen at this
// call site, so de-confounding by editing the argument (rag.RETRIEVAL_TOP_K ->
// rag.RETRIEVAL_POOL_CAP) is visible only from armcontext. The other half -
// equalizing the constants themselves - is pinned by
// rag.TestRetrievalArmsInjectDifferentExcerptVolumes. Neither test sees both
// routes, which is why there are two.
//
// The k asserted on is the one production passes, captured from the real
// BuildProfile call rather than supplied by the fake, so this cannot pass by
// agreeing with itself.
func TestRetrievalArmsRequestDifferentExcerptBudgets(t *testing.T) {
	stubLoaders(t)

	requestedK := -1
	loadRagContext = func(query string, k int) ([]string, error) {
		requestedK = k
		return []string{"excerpt"}, nil
	}

	if _, err := BuildProfile(hackernews_classifier.ArmRAG, testStories); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if requestedK != rag.RETRIEVAL_TOP_K {
		t.Errorf("arm 2 requested k = %d, want rag.RETRIEVAL_TOP_K = %d", requestedK, rag.RETRIEVAL_TOP_K)
	}
	// Arm 3 has no k argument: rag.RetrievePooledContext reads the cap itself, so
	// the cap IS arm 3's excerpt budget as seen from this call site.
	if requestedK == rag.RETRIEVAL_POOL_CAP {
		t.Fatalf("both retrieval arms now budget %d excerpts; if this was a deliberate de-confound, re-run eval 2 and eval 3 and drop the confound caveats from README/CLAUDE.md before deleting this test", requestedK)
	}
}

func TestBuildProfileUnknownArm(t *testing.T) {
	stubLoaders(t)

	if _, err := BuildProfile(hackernews_classifier.Arm(99), testStories); err == nil {
		t.Fatal("expected an error for an unknown arm, got nil")
	}
}
