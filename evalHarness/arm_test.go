package evalHarness

import (
	"errors"
	"testing"

	"github.com/shrirambalakrishnan/tech-news/hackernews_classifier"
	"github.com/shrirambalakrishnan/tech-news/profile"
)

func TestBuildProfileForArm(t *testing.T) {
	t.Run("ArmGeneric returns an empty profile without loading", func(t *testing.T) {
		loadUserContext = func(path string) (profile.UserContext, error) {
			t.Fatal("ArmGeneric must not load user context")
			return profile.UserContext{}, nil
		}
		defer func() { loadUserContext = profile.LoadUserContext }()

		p, err := buildProfileForArm(hackernews_classifier.ArmGeneric)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !p.IsEmpty() {
			t.Fatalf("expected empty profile, got %+v", p)
		}
	})

	t.Run("ArmInterests maps the loaded context", func(t *testing.T) {
		loadUserContext = func(path string) (profile.UserContext, error) {
			return profile.UserContext{Summary: "backend work", Interests: []string{"Go", "Spanner"}}, nil
		}
		defer func() { loadUserContext = profile.LoadUserContext }()

		p, err := buildProfileForArm(hackernews_classifier.ArmInterests)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.Summary != "backend work" || len(p.Interests) != 2 {
			t.Fatalf("expected loaded context mapped into the profile, got %+v", p)
		}
	})

	t.Run("ArmInterests errors when the context is missing (does not score arm 0)", func(t *testing.T) {
		loadUserContext = func(path string) (profile.UserContext, error) {
			return profile.UserContext{}, errors.New("file not found")
		}
		defer func() { loadUserContext = profile.LoadUserContext }()

		if _, err := buildProfileForArm(hackernews_classifier.ArmInterests); err == nil {
			t.Fatal("expected an error when the distilled profile is missing, got nil")
		}
	})

	t.Run("ArmRAG returns an empty profile without loading", func(t *testing.T) {
		loadUserContext = func(path string) (profile.UserContext, error) {
			t.Fatal("ArmRAG must not load the distilled user context")
			return profile.UserContext{}, nil
		}
		defer func() { loadUserContext = profile.LoadUserContext }()

		// Arm 2's RetrievedExcerpts are query-dependent, so classifyInBatches fills
		// them per batch. A retrieval failure errors there (see eval_test.go),
		// so the arm still fails loudly rather than scoring arm 0.
		p, err := buildProfileForArm(hackernews_classifier.ArmRAG)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !p.IsEmpty() {
			t.Fatalf("expected empty profile, got %+v", p)
		}
	})
}
