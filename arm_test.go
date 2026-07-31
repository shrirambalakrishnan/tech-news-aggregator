package main

import (
	"testing"

	"github.com/shrirambalakrishnan/tech-news/hackernews_classifier"
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

// Per-arm profile construction now lives in the armcontext package (shared by
// this package and evalHarness); see armcontext/build_test.go.
