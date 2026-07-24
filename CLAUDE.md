# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Go CLI that fetches HackerNews front-page stories, uses the Claude API to classify which ones are "technical computer science" stories worth reading, and appends the matches (as Markdown links) to `stories.md` with a timestamp. It is meant to run unattended on macOS via `launchd` every 4 hours.

## Commands

```bash
go run .                        # build + run, arm 0 (requires ANTHROPIC_API_KEY in env)
go run . 1                      # run under arm 1 (interests; needs prebuild's JSON)
go run . eval 0                 # eval under a specific arm (arm is REQUIRED for eval)
go test ./...                   # run all tests
go test ./hackernews_classifier/ -run TestClassifyTechNewsStory   # single test, single package
./tech-news-run.sh              # production entrypoint: pulls API key from macOS Keychain, then `go run .` (arm 0)
```

CI (`.github/workflows/test.yml`) runs `go test ./...` on pushes/PRs to `main`. There is no linter configured.

`ANTHROPIC_API_KEY` is read via `os.Getenv` in `claudeapi`. For local runs export it directly; in production `tech-news-run.sh` fetches it from the Keychain (`security find-generic-password -s ANTHROPIC_API_KEY`). See README.md for the Keychain + launchd install steps.

## Architecture

**Explicit arm selection (issue #11).** The classification flow is chosen by an
explicit `arm` argument, not by what happens to be on disk. `main.parseArm`
validates the CLI arg; `arm 0` = generic/static prompt with no profile (the
production default, kept for cron-safety), `arm 1` = interests injected from the
distilled JSON, `arm 2` = RAG (stubbed, always errors this iteration). Both `main`
and `evalHarness` own a `buildProfileForArm(arm)` that initializes the arm's
`UserProfile` and **errors (non-zero exit) when the arm's data is missing** — it
does *not* fail soft to arm 0. `ClassifyTechNewsStory(arm, stories, profile)` and
`ConstructPromptSystemAttribute(arm, profile)` switch on the arm, so the profile's
emptiness no longer selects the flow. Normal runs default the arm to 0; `eval`
requires an explicit arm so a run meant for one arm can't silently score another.

Pipeline (entry point `main.go` → `GetMyHackerNewsStories(arm)`):

1. **`main` package** (repo root): `hackernews.go` fetches stories from the Algolia HN API (`hn.algolia.com`, `tags=front_page`) **page by page** and **cumulates** them into one slice — `GetHackerNewsStories()` loops `HACKERNEWS_NUM_PAGES_TO_QUERY` pages, each returning `HACKERNEWS_HITS_PER_PAGE` hits (defaults: 1 page × 30 = 30 stories) — then filters them. `main.go` writes results to `stories.md`. `apiHelper.go` holds a generic `PostJSON` helper (currently unused — staged for a future refactor of the per-package HTTP code).
2. **`hackernews_classifier` package**: builds the system + message prompts and calls Claude to classify a `[]StoryDetail` (id + title), returning the IDs deemed technical. **All cumulated stories go to Claude in a single API call per run** — `FilterHackerNewsStoriesByTitle` flattens the whole fetched slice into one `ClassifyTechNewsStory` call; it is *not* batched by page. The Algolia page size (`HACKERNEWS_HITS_PER_PAGE`) only controls how many stories are *fetched*, not the Claude batch size; with the defaults the single call happens to carry ~30 stories. `ConstructPromptSystemAttribute(arm, UserProfile)` returns the hardcoded static ruleset (`staticClassificationPrompt`) for `arm 0` and the interests-injected prompt for `arm 1` — the arm selects the flow, not the profile's emptiness. `UserProfile` is the classifier's own slim input contract (signal fields only) — `main` maps `profile.UserContext` into it (in `buildProfileForArm`), so the classifier never imports `profile`.
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

> **Superseded by issue #11 (explicit arm selection):** the fail-soft behaviour above (missing artifact → empty profile → static rules) is now only how **arm 0** behaves *by choice*. Flow selection is explicit via the `arm` argument; `buildProfileForArm` **errors instead of failing soft** when arm 1's JSON is missing (see the Architecture section). `ConstructPromptSystemAttribute`/`ClassifyTechNewsStory` now take the arm and switch on it rather than on `IsEmpty()`.

### Design decisions (settled in discussion)

- **Artifact format is a hybrid**, not bare keywords: a prose `summary` (for Claude to reason/generalize over) **plus** a flat `interests` array (inspectable/diffable), wrapped with metadata (`schema_version`, `generated_at`, `source`, `model`) for provenance and forward-compatibility. There is no useful "binary" form — everything stored is text the model reads.
- **Why prebuild instead of sending READMEs raw every run:** the classify job runs every 4h (~180×/month); re-sending the same READMEs re-pays for those tokens each run. Prebuild extracts once and injects a ~300-token profile per call. Prompt caching can't help (max TTL 1h < 4h cadence). Cost/token table is in `README.md` → *Cost model*. The context window is *not* the binding constraint (30 READMEs ≈ 23% of Haiku's 200K).
- **Prebuilt context is a lossy proxy, not equivalent to raw.** Distillation drops nuance, the extraction prompt imposes a lens, and the LLM is non-deterministic — so we cannot guarantee the same classifications as passing raw READMEs into the classify call. This is an accepted trade for cost/stability/inspectability. The fidelity gap should be *measured* via an offline eval (README roadmap), not assumed.
- The `profile` package follows the same function-variable DI convention as the rest of the repo (`constructUserContextSystemAttribute`, `constructUserContextMessageAttribute`, `claudeMessageApiCall`).

## Eval harness (branch `feature/7-eval-harness`)

Offline eval of the classifier against a hand-labelled dataset, to measure classifier-vs-ground-truth quality (and, later, the prebuilt-vs-raw fidelity gap from the roadmap).

`RunEval(arm)` runs **exactly one arm** (issue #11): the CLI requires it (`go run . eval <arm>`, no default), and `evalHarness.buildProfileForArm` errors — non-zero exit — when arm 1's distilled JSON is missing, so an eval meant for arm 1 never silently scores arm 0.

- **Labelled data:** `evalHarness/hn-responses-labelled.json` — an array of `{objectID, story_id, title, url, label}` where `label` is `1` (relevant) or `0` (not). 341 stories, 35 positive (~10% prevalence). **Git-ignored** (backup kept in notes; a fixture, not source of truth).
- **Metrics (phase 1 — deliberately bare minimum):** the `Metrics` struct reports only the confusion matrix (`TP/FP/TN/FN`), **precision**, **recall**, and the **false-positive / false-negative title lists**. That is the irreducible set to evaluate correctly: the four cells carry every count, precision/recall cover the two independent failure modes, and the lists show *what* to fix. Accuracy, F1, and the imbalance-aware extras (MCC, balanced accuracy, specificity, NPV, FPR, FNR, kappa, F2) were intentionally **cut** — accuracy actively misleads on 90/10 data, and the rest are good-to-have summaries/complements (some, e.g. FPR/FNR, are literal restatements of recall/specificity). **eval v2:** re-add a single robust score (F1 first, then MCC/balanced accuracy) when *comparing* classifier variants (static vs profile vs RAG), which is when one ranking number earns its keep. `Evaluate(predictedIDs, dataset)` is pure (no I/O) so the metric math is unit-tested with hand-computed numbers.
- **Run shape — batch 30 (locked decision):** the eval replays the dataset through the classifier in chunks of `HACKERNEWS_HITS_PER_PAGE` (~30) per Claude call, then aggregates the predicted IDs across chunks before scoring all 341. Rationale: production sends *all* cumulated stories to Claude in **one** call (see Architecture), which with the defaults is ~30 stories — so chunking the eval at 30 makes each Claude call the same size production actually uses, keeping the measured numbers transferable. It also stays well under `claudeapi`'s `MaxTokens: 1024` output cap: sending all 341 at once can truncate the returned JSON ID array, which `json.Unmarshal` then fails to parse → the classifier returns `[]` → recall silently scores 0. The LLM is non-deterministic, so a single run is a point estimate; run *k* times for mean ± stddev when the numbers look unstable.
- **Token & cost per eval run** (model is Haiku 4.5 — `claudeapi`'s `ANTHROPIC_MODEL_NAME`; **$1.00 / 1M input, $5.00 / 1M output**). With ~30 stories/call and `ceil(341/30) = 12` calls: ~1,100 input tokens/call (≈300 system + ≈800 message of `Story Id: … , Story Title: …` lines) and ≈50 output tokens/call → **≈13K input + ≈0.6K output ≈ $0.016 per run** (~1.6¢). Batching repeats the system prompt across all 12 calls (~$0.003 of that total vs. a single one-shot call — negligible). A *k*-run variance pass costs ~k×. These are estimates from the README token rule of thumb; only `count_tokens` gives exact figures.
