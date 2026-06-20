package profile

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/shrirambalakrishnan/tech-news/claudeapi"
)

func TestConstructUserContextMessageAttribute(t *testing.T) {
	t.Run("wraps each README with a numbered header", func(t *testing.T) {
		readmes := []string{"first readme", "second readme"}

		expected := "===== Repository README 1 =====\nfirst readme\n\n===== Repository README 2 =====\nsecond readme\n\n"

		got := ConstructUserContextMessageAttribute(readmes)
		if got != expected {
			t.Fatalf("expected message to be %q, got %q", expected, got)
		}
	})
}

func TestExtractUserContext(t *testing.T) {
	t.Run("calls claudeMessageApiCall with composed prompt", func(t *testing.T) {
		constructUserContextSystemAttribute = func() string { return "system prompt" }
		defer func() { constructUserContextSystemAttribute = ConstructUserContextSystemAttribute }()

		constructUserContextMessageAttribute = func(readmes []string) string { return "message prompt" }
		defer func() { constructUserContextMessageAttribute = ConstructUserContextMessageAttribute }()

		var calledWith claudeapi.PromptInput
		claudeMessageApiCall = func(prompt claudeapi.PromptInput, response *claudeapi.Response) error {
			calledWith = prompt
			response.Content = []claudeapi.ResponseContent{{Type: "text", Text: `{"summary":"s","interests":["Go"]}`}}
			return nil
		}
		defer func() { claudeMessageApiCall = claudeapi.ClaudeMessageApiCall }()

		ExtractUserContext([]string{"readme"}, "octocat")

		expected := claudeapi.PromptInput{System: "system prompt", Message: "message prompt"}
		if calledWith != expected {
			t.Fatalf("expected claudeMessageApiCall called with %v, got %v", expected, calledWith)
		}
	})

	t.Run("parses summary and interests and wraps with metadata", func(t *testing.T) {
		claudeMessageApiCall = func(prompt claudeapi.PromptInput, response *claudeapi.Response) error {
			response.Content = []claudeapi.ResponseContent{{Type: "text", Text: `{"summary":"Backend dev","interests":["Go","compilers"]}`}}
			return nil
		}
		defer func() { claudeMessageApiCall = claudeapi.ClaudeMessageApiCall }()

		got, err := ExtractUserContext([]string{"readme"}, "octocat")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got.Summary != "Backend dev" {
			t.Fatalf("expected summary %q, got %q", "Backend dev", got.Summary)
		}
		if !reflect.DeepEqual(got.Interests, []string{"Go", "compilers"}) {
			t.Fatalf("expected interests [Go compilers], got %v", got.Interests)
		}
		if got.SchemaVersion != USER_CONTEXT_SCHEMA_VERSION {
			t.Fatalf("expected schema version %d, got %d", USER_CONTEXT_SCHEMA_VERSION, got.SchemaVersion)
		}
		if got.Source != "github:octocat" {
			t.Fatalf("expected source %q, got %q", "github:octocat", got.Source)
		}
		if got.Model != claudeapi.ANTHROPIC_MODEL_NAME {
			t.Fatalf("expected model %q, got %q", claudeapi.ANTHROPIC_MODEL_NAME, got.Model)
		}
		if got.GeneratedAt == "" {
			t.Fatalf("expected GeneratedAt to be set")
		}
	})

	t.Run("parses JSON wrapped in markdown code fences", func(t *testing.T) {
		claudeMessageApiCall = func(prompt claudeapi.PromptInput, response *claudeapi.Response) error {
			response.Content = []claudeapi.ResponseContent{{Type: "text", Text: "```json\n{\"summary\":\"Backend dev\",\"interests\":[\"Go\"]}\n```"}}
			return nil
		}
		defer func() { claudeMessageApiCall = claudeapi.ClaudeMessageApiCall }()

		got, err := ExtractUserContext([]string{"readme"}, "octocat")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Summary != "Backend dev" || !reflect.DeepEqual(got.Interests, []string{"Go"}) {
			t.Fatalf("expected fenced JSON to parse, got %+v", got)
		}
	})

	t.Run("returns error on empty content", func(t *testing.T) {
		claudeMessageApiCall = func(prompt claudeapi.PromptInput, response *claudeapi.Response) error {
			response.Content = []claudeapi.ResponseContent{}
			return nil
		}
		defer func() { claudeMessageApiCall = claudeapi.ClaudeMessageApiCall }()

		if _, err := ExtractUserContext([]string{"readme"}, "octocat"); err == nil {
			t.Fatalf("expected error on empty content, got nil")
		}
	})

	t.Run("returns error on unparseable JSON", func(t *testing.T) {
		claudeMessageApiCall = func(prompt claudeapi.PromptInput, response *claudeapi.Response) error {
			response.Content = []claudeapi.ResponseContent{{Type: "text", Text: "not json"}}
			return nil
		}
		defer func() { claudeMessageApiCall = claudeapi.ClaudeMessageApiCall }()

		if _, err := ExtractUserContext([]string{"readme"}, "octocat"); err == nil {
			t.Fatalf("expected error on unparseable JSON, got nil")
		}
	})
}

func TestWriteAndLoadUserContext(t *testing.T) {
	t.Run("round-trips the user context through disk", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "user_context.json")

		want := UserContext{
			SchemaVersion: USER_CONTEXT_SCHEMA_VERSION,
			GeneratedAt:   "2026-06-19T10:00:00Z",
			Source:        "github:octocat",
			Model:         "claude-haiku-4-5-20251001",
			Summary:       "Backend dev",
			Interests:     []string{"Go", "compilers"},
		}

		if err := WriteUserContext(want, path); err != nil {
			t.Fatalf("unexpected error writing: %v", err)
		}

		got, err := LoadUserContext(path)
		if err != nil {
			t.Fatalf("unexpected error loading: %v", err)
		}

		if !reflect.DeepEqual(got, want) {
			t.Fatalf("expected %+v, got %+v", want, got)
		}
	})

	t.Run("returns error when the file does not exist", func(t *testing.T) {
		if _, err := LoadUserContext(filepath.Join(t.TempDir(), "missing.json")); err == nil {
			t.Fatalf("expected error for missing file, got nil")
		}
	})
}
