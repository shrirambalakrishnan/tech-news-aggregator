package main

import (
	"errors"
	"testing"

	"github.com/shrirambalakrishnan/tech-news/hackernews_classifier"
	"github.com/shrirambalakrishnan/tech-news/profile"
)

func TestParseArm(t *testing.T) {
	valid := map[string]hackernews_classifier.Arm{
		"0": hackernews_classifier.ArmGeneric,
		"1": hackernews_classifier.ArmInterests,
		"2": hackernews_classifier.ArmRAG,
	}
	for in, want := range valid {
		got, err := parseArm(in)
		if err != nil {
			t.Fatalf("parseArm(%q) returned error: %v", in, err)
		}
		if got != want {
			t.Fatalf("parseArm(%q) = %d, want %d", in, got, want)
		}
	}

	for _, in := range []string{"", "abc", "-1", "3", "1.0"} {
		if _, err := parseArm(in); err == nil {
			t.Fatalf("parseArm(%q) expected error, got nil", in)
		}
	}
}

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

	t.Run("ArmInterests errors when the context is missing", func(t *testing.T) {
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

		// Arm 2's RetrievedExcerpts are query-dependent, so they are filled per
		// batch in FilterHackerNewsStoriesByTitle, not here. A missing index
		// errors there (covered in hackernews_test.go), so the arm still fails
		// loudly rather than failing soft.
		p, err := buildProfileForArm(hackernews_classifier.ArmRAG)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !p.IsEmpty() {
			t.Fatalf("expected empty profile, got %+v", p)
		}
	})

	t.Run("unknown arm errors", func(t *testing.T) {
		if _, err := buildProfileForArm(hackernews_classifier.Arm(99)); err == nil {
			t.Fatal("expected an error for an unknown arm, got nil")
		}
	})
}
