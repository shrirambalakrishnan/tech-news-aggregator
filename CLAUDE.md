# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Go CLI that fetches HackerNews front-page stories, uses the Claude API to classify which ones are "technical computer science" stories worth reading, and appends the matches (as Markdown links) to `stories.md` with a timestamp. It is meant to run unattended on macOS via `launchd` every 4 hours.

## Commands

```bash
go run .                        # build + run (requires ANTHROPIC_API_KEY in env)
go test ./...                   # run all tests
go test ./hackernews_classifier/ -run TestClassifyTechNewsStory   # single test, single package
./tech-news-run.sh              # production entrypoint: pulls API key from macOS Keychain, then `go run .`
```

CI (`.github/workflows/test.yml`) runs `go test ./...` on pushes/PRs to `main`. There is no linter configured.

`ANTHROPIC_API_KEY` is read via `os.Getenv` in `claudeapi`. For local runs export it directly; in production `tech-news-run.sh` fetches it from the Keychain (`security find-generic-password -s ANTHROPIC_API_KEY`). See README.md for the Keychain + launchd install steps.

## Architecture

Pipeline (entry point `main.go` → `GetMyHackerNewsStories()`):

1. **`main` package** (repo root): `hackernews.go` fetches stories from the Algolia HN API (`hn.algolia.com`, `tags=front_page`, paginated), then filters them. `main.go` writes results to `stories.md`. `apiHelper.go` holds a generic `PostJSON` helper (currently unused — staged for a future refactor of the per-package HTTP code).
2. **`hackernews_classifier` package**: builds the system + message prompts and calls Claude to classify a `[]StoryDetail` (id + title), returning the IDs deemed technical. The classification rules live as a hardcoded string in `ConstructPromptSystemAttribute`.
3. **`claudeapi` package**: thin Anthropic Messages API client (`POST /v1/messages`). Model and request shape are hardcoded here (`ANTHROPIC_MODEL_NAME`).
4. **`profile` package**: fetches a GitHub user's repos and their READMEs. Not yet wired into the pipeline — `ExtractGithubProfile()` is commented out in `main.go`. This is the foundation for the in-progress feature described below.

Packages depend downward only: `main` → `hackernews_classifier` → `claudeapi`. `profile` is standalone.

## Key convention: function-variable dependency injection

This is the central testing pattern and is used pervasively — follow it for any new code. Each function's external calls are routed through a package-level `var` initialized to the real function:

```go
var classifyTechNewsStory = hackernews_classifier.ClassifyTechNewsStory
var claudeMessageApiCall = claudeapi.ClaudeMessageApiCall
```

Tests swap these vars for fakes and restore them with `defer`:

```go
classifyTechNewsStory = func(stories []hackernews_classifier.StoryDetail) []int { return []int{1, 3} }
defer func() { classifyTechNewsStory = hackernews_classifier.ClassifyTechNewsStory }()
```

This keeps tests free of network calls. When you add a function that calls another (especially across packages or to the network), expose it through such a var so callers can be tested in isolation. Tunable config like `HACKERNEWS_NUM_PAGES_TO_QUERY` and `HACKERNEWS_HITS_PER_PAGE` are likewise package-level mutable vars that tests reassign.

## In-progress feature (branch `feature/3-pre-build-user-context`)

`agents/issue_3.md` is the spec. Goal: replace the hardcoded classification rules with rules derived from the user's actual interests. A `prebuild` step uses the `profile` package to fetch the user's GitHub READMEs, asks the LLM to extract interest keywords, and writes them to `profile/user_context.json` (with a `generated_at` timestamp). The main run then injects those keywords into `ConstructPromptSystemAttribute`. `GITHUB_USERNAME` (see `.env.example`) selects the GitHub user.
