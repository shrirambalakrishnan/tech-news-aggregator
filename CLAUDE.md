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
2. **`hackernews_classifier` package**: builds the system + message prompts and calls Claude to classify a `[]StoryDetail` (id + title), returning the IDs deemed technical. `ConstructPromptSystemAttribute(UserProfile)` injects the user's interests when a profile is supplied, and falls back to a hardcoded static ruleset (`staticClassificationPrompt`) when the profile is empty. `UserProfile` is the classifier's own slim input contract (signal fields only) — `main` maps `profile.UserContext` into it, so the classifier never imports `profile`.
3. **`claudeapi` package**: thin Anthropic Messages API client (`POST /v1/messages`). Model and request shape are hardcoded here (`ANTHROPIC_MODEL_NAME`).
4. **`profile` package**: fetches a GitHub user's repos and their READMEs (`github.go`), then extracts an interest profile from them via the LLM and reads/writes `profile/user_context.json` (`context.go`). Wired in as a **prebuild step**: `go run . prebuild` calls `ExtractGithubProfile()` and exits; the normal run skips it.

Packages depend downward only: `main` → `hackernews_classifier` → `claudeapi`, and `main` → `profile` → `claudeapi`.

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

`agents/issue_3.md` is the spec. Goal: replace the hardcoded classification rules with rules derived from the user's actual interests.

**Step 1 (done):** `profile/github.go` fetches the user's repos + READMEs. `GITHUB_USERNAME` (see `.env.example`) selects the user. We deliberately keep only README text for now — repo name/description/topics can be added later.

**Step 2 (done):** `profile/context.go` sends all READMEs in one LLM call, extracts an interest profile, and writes `profile/user_context.json` via the `prebuild` entrypoint in `main.go`. The artifact is **git-ignored** (a regenerable cache, not source of truth) and read back with `LoadUserContext` — callers must **fail soft** (fall back to the static rules) when it's missing, since prebuild may not have run.

**Step 3 (done):** `ConstructPromptSystemAttribute` now takes a `hackernews_classifier.UserProfile` (`summary` + `interests`) and builds an interest-driven prompt, falling back to the static ruleset when the profile `IsEmpty()`. `FilterHackerNewsStoriesByTitle` (in `hackernews.go`) calls `loadUserContext` (DI var → `profile.LoadUserContext`), maps `profile.UserContext` → `UserProfile`, and fails soft (empty profile → static rules) when the artifact is missing. To preserve the downward-only dependency rule, the classifier defines `UserProfile` itself rather than importing `profile`; `main` owns the mapping and drops the provenance metadata.

### Design decisions (settled in discussion)

- **Artifact format is a hybrid**, not bare keywords: a prose `summary` (for Claude to reason/generalize over) **plus** a flat `interests` array (inspectable/diffable), wrapped with metadata (`schema_version`, `generated_at`, `source`, `model`) for provenance and forward-compatibility. There is no useful "binary" form — everything stored is text the model reads.
- **Why prebuild instead of sending READMEs raw every run:** the classify job runs every 4h (~180×/month); re-sending the same READMEs re-pays for those tokens each run. Prebuild extracts once and injects a ~300-token profile per call. Prompt caching can't help (max TTL 1h < 4h cadence). Cost/token table is in `README.md` → *Cost model*. The context window is *not* the binding constraint (30 READMEs ≈ 23% of Haiku's 200K).
- **Prebuilt context is a lossy proxy, not equivalent to raw.** Distillation drops nuance, the extraction prompt imposes a lens, and the LLM is non-deterministic — so we cannot guarantee the same classifications as passing raw READMEs into the classify call. This is an accepted trade for cost/stability/inspectability. The fidelity gap should be *measured* via an offline eval (README roadmap), not assumed.
- The `profile` package follows the same function-variable DI convention as the rest of the repo (`constructUserContextSystemAttribute`, `constructUserContextMessageAttribute`, `claudeMessageApiCall`).
