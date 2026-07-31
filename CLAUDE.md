# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Go CLI that fetches HackerNews front-page stories, uses the Claude API to classify which ones are "technical computer science" stories worth reading, and appends the matches (as Markdown links) to `stories.md` with a timestamp. It is meant to run unattended on macOS via `launchd` every 4 hours.

## Commands

```bash
go run .                        # build + run, arm 0 (requires ANTHROPIC_API_KEY in env)
go run . 1                      # run under arm 1 (interests; needs prebuild's JSON)
go run . 2                      # run under arm 2 (RAG; needs embed's corpus index)
go run . prebuild               # extract GitHub interest profile -> profile/user_context.json (occasional)
go run . embed                  # chunk+embed profile/corpus -> profile/corpus_index.json (requires VOYAGE_API_KEY; ~20 min on Voyage free tier)
go run . eval 0                 # eval under a specific arm (arm is REQUIRED for eval; 0|1|2)
go test ./...                   # run all tests
go test ./hackernews_classifier/ -run TestClassifyTechNewsStory   # single test, single package
./tech-news-run.sh              # production entrypoint: pulls API key from macOS Keychain, then `go run .` (arm 0)
```

CI (`.github/workflows/test.yml`) runs `go test ./...` on pushes/PRs to `main`. There is no linter configured.

### Secrets & environment

| Name | Type | Read by | Where it lives |
|------|------|---------|----------------|
| `ANTHROPIC_API_KEY` | secret | `claudeapi` (`os.Getenv`) — every Claude call | macOS Keychain |
| `VOYAGE_API_KEY` | secret | `voyageapi` (`os.Getenv`) — the `go run . embed` step only | macOS Keychain |
| `GITHUB_USERNAME` | non-secret | `profile/github.go` (`os.Getenv`) — whose READMEs to fetch | `.env` (see `.env.example`) |

For local runs export the keys directly; in production `tech-news-run.sh` fetches secrets from the macOS Keychain (`security find-generic-password -a "$USER" -s <NAME> -w`) and exports them before `go run .`. **The runner exports only `ANTHROPIC_API_KEY`** — `VOYAGE_API_KEY` is needed only by the one-off `go run . embed` step (not the 4-hourly run), so export it manually for that: `export VOYAGE_API_KEY=$(security find-generic-password -a "$USER" -s VOYAGE_API_KEY -w)`. See README.md for the Keychain + launchd install steps.

## Architecture

**Explicit arm selection (issue #11).** The classification flow is chosen by an
explicit `arm` argument, not by what happens to be on disk. `main.parseArm`
validates the CLI arg; `arm 0` = generic/static prompt with no profile (the
production default, kept for cron-safety), `arm 1` = interests injected from the
distilled JSON, `arm 2` = RAG (corpus chunks retrieved from the embed step's
index; live in **both** the classify path and `evalHarness`). Every arm's context
is built by **one shared function**, `armcontext.BuildProfile(arm, stories)`,
which returns the arm's `UserProfile` and **errors (non-zero exit) when the arm's
data is missing** — it does *not* fail soft to arm 0. The `stories` parameter is
arm 2's retrieval query (their titles, newline-joined); it is a parameter
precisely so that arm 2 builds in the same switch as arms 0 and 1 instead of
being patched in afterwards at each call site. `main.FilterHackerNewsStoriesByTitle`
(production, once per run) and `evalHarness.classifyInBatches` (eval, once per
batch) both call it through a `buildProfileForArm` DI var, so the eval measures
production's exact context — including its retrieval shape — by construction
rather than by two hand-synced copies.
`ClassifyTechNewsStory(arm, stories, profile)` and
`ConstructPromptSystemAttribute(arm, profile)` switch on the arm, so the profile's
emptiness no longer selects the flow. Normal runs default the arm to 0; `eval`
requires an explicit arm so a run meant for one arm can't silently score another.

Pipeline (entry point `main.go` → `GetMyHackerNewsStories(arm)`):

1. **`main` package** (repo root): `hackernews.go` fetches stories from the Algolia HN API (`hn.algolia.com`, `tags=front_page`) **page by page** and **cumulates** them into one slice — `GetHackerNewsStories()` loops `HACKERNEWS_NUM_PAGES_TO_QUERY` pages, each returning `HACKERNEWS_HITS_PER_PAGE` hits (defaults: 1 page × 30 = 30 stories) — then filters them. `main.go` writes results to `stories.md`. `apiHelper.go` holds a generic `PostJSON` helper (currently unused — staged for a future refactor of the per-package HTTP code).
2. **`hackernews_classifier` package**: builds the system + message prompts and calls Claude to classify a `[]StoryDetail` (id + title), returning the IDs deemed technical. **All cumulated stories go to Claude in a single API call per run** — `FilterHackerNewsStoriesByTitle` flattens the whole fetched slice into one `ClassifyTechNewsStory` call; it is *not* batched by page. The Algolia page size (`HACKERNEWS_HITS_PER_PAGE`) only controls how many stories are *fetched*, not the Claude batch size; with the defaults the single call happens to carry ~30 stories. `ConstructPromptSystemAttribute(arm, UserProfile)` returns the hardcoded static ruleset (`staticClassificationPrompt`) for `arm 0` and the interests-injected prompt for `arm 1` — the arm selects the flow, not the profile's emptiness. `UserProfile` is the classifier's own slim input contract (signal fields only) — `armcontext` maps `profile.UserContext` into it, so the classifier never imports `profile`.
3. **`claudeapi` package**: thin Anthropic Messages API client (`POST /v1/messages`). Model and request shape are hardcoded here (`ANTHROPIC_MODEL_NAME`).
4. **`profile` package**: fetches a GitHub user's repos and their READMEs (`github.go`), then extracts an interest profile from them via the LLM and reads/writes `profile/user_context.json` (`context.go`). Wired in as a **prebuild step**: `go run . prebuild` calls `ExtractGithubProfile()` and exits; the normal run skips it.
5. **`voyageapi` package** (Approach 3): thin Voyage AI embeddings client (`POST /v1/embeddings`), mirroring `claudeapi`. `EmbedDocuments([]string) ([][]float32, error)` token-batches inputs and **paces requests for Voyage's no-payment tier** (3 RPM / 10K TPM): token-bounded batches, an inter-request delay, and 429 retry-with-backoff (all tunable package vars). Reads `VOYAGE_API_KEY`. Organized **one file per layer**: `voyage.go` (PUBLIC API: `EmbedDocuments` + the `embedBatch` DI seam), `ratelimit.go` (POLICY: free-tier config vars, `planBatches`, `pacingDelay`, retry), `client.go` (TRANSPORT: wire types + `embedBatchHTTP`).
6. **`rag` package** (Approach 3): organized **one file per pipeline stage** — `corpus.go` is the INPUT stage (`CORPUS_DIR`, `readCorpusFiles`, `inferType`; types are stamped at read time so later stages never inspect filenames); `chunk.go` is the TRANSFORM stage (pure `ChunkText(text, window, overlap)`, fixed-size word windows, 800/120); `index.go` is the OUTPUT stage (the `CorpusIndex` artifact: provenance metadata + `[]Chunk`, with `Write`/`LoadCorpusIndex`); `build.go` is the PIPELINE (the `embedDocuments` DI seam, `buildIndex`, and `BuildCorpusIndex()` orchestrating read `profile/corpus/` → chunk → embed → write `profile/corpus_index.json`); `retrieve.go` is the RETRIEVAL stage (pure `cosineSimilarity` + `topKBySimilarity`, and `RetrieveContext(query, k)` = load index → embed query via `voyageapi.EmbedQuery` → top-k chunk texts; `RETRIEVAL_TOP_K` = 5). Wired as the **embed step**: `go run . embed`. Index is git-ignored (regenerable, like `user_context.json`), but under explicit arm selection a **missing index is fatal for arm 2** rather than fail-soft — see Approach 3 below. **Retrieval is wired into both the classify path and the eval arm.**

7. **`armcontext` package**: the single place that answers "what does this arm classify against?". `BuildProfile(arm, stories)` switches on the arm — empty profile for arm 0, the distilled summary/interests for arm 1, the excerpts `rag.RetrieveContext` returns for arm 2 — and errors (never a degraded profile) when the arm's artifact is missing. `retrievalQuery` (unexported) owns arm 2's retrieval key: the batch's titles, newline-joined. It exists so `main` and `evalHarness` share one implementation instead of two copies that must be kept in sync for the eval's numbers to transfer; both reach it through a `buildProfileForArm` DI var, which is also what their tests swap (per-arm construction is tested once, in `armcontext`).

Packages depend downward only: `main` → `hackernews_classifier` → `claudeapi`, `main` → `profile` → `claudeapi`, `main` → `rag` → `voyageapi`, and `main`/`evalHarness` → `armcontext` → `{hackernews_classifier, profile, rag}`.

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

`RunEval(arm)` runs **exactly one arm** (issue #11): the CLI requires it (`go run . eval <arm>`, no default), and the shared `armcontext.BuildProfile` errors — non-zero exit — when arm 1's distilled JSON is missing, so an eval meant for arm 1 never silently scores arm 0.

- **Labelled data:** `evalHarness/hn-responses-labelled.json` — an array of `{objectID, story_id, title, url, label}` where `label` is `1` (relevant) or `0` (not). 341 stories, 35 positive (~10% prevalence). **Git-ignored** (backup kept in notes; a fixture, not source of truth).
- **Metrics (phase 1 — deliberately bare minimum):** the `Metrics` struct reports only the confusion matrix (`TP/FP/TN/FN`), **precision**, **recall**, and the **false-positive / false-negative title lists**. That is the irreducible set to evaluate correctly: the four cells carry every count, precision/recall cover the two independent failure modes, and the lists show *what* to fix. Accuracy, F1, and the imbalance-aware extras (MCC, balanced accuracy, specificity, NPV, FPR, FNR, kappa, F2) were intentionally **cut** — accuracy actively misleads on 90/10 data, and the rest are good-to-have summaries/complements (some, e.g. FPR/FNR, are literal restatements of recall/specificity). **eval v2:** re-add a single robust score (F1 first, then MCC/balanced accuracy) when *comparing* classifier variants (static vs profile vs RAG), which is when one ranking number earns its keep. `Evaluate(predictedIDs, dataset)` is pure (no I/O) so the metric math is unit-tested with hand-computed numbers.
- **Run shape — batch 30 (locked decision):** the eval replays the dataset through the classifier in chunks of `HACKERNEWS_HITS_PER_PAGE` (~30) per Claude call, then aggregates the predicted IDs across chunks before scoring all 341. Rationale: production sends *all* cumulated stories to Claude in **one** call (see Architecture), which with the defaults is ~30 stories — so chunking the eval at 30 makes each Claude call the same size production actually uses, keeping the measured numbers transferable. It also stays well under `claudeapi`'s `MaxTokens: 1024` output cap: sending all 341 at once can truncate the returned JSON ID array, which `json.Unmarshal` then fails to parse → the classifier returns `[]` → recall silently scores 0. The LLM is non-deterministic, so a single run is a point estimate; run *k* times for mean ± stddev when the numbers look unstable.
- **Token & cost per eval run** (model is Haiku 4.5 — `claudeapi`'s `ANTHROPIC_MODEL_NAME`; **$1.00 / 1M input, $5.00 / 1M output**). With ~30 stories/call and `ceil(341/30) = 12` calls: ~1,100 input tokens/call (≈300 system + ≈800 message of `Story Id: … , Story Title: …` lines) and ≈50 output tokens/call → **≈13K input + ≈0.6K output ≈ $0.016 per run** (~1.6¢). Batching repeats the system prompt across all 12 calls (~$0.003 of that total vs. a single one-shot call — negligible). A *k*-run variance pass costs ~k×. These are estimates from the README token rule of thumb; only `count_tokens` gives exact figures.

## Approach 3 — RAG experiment (planned)

**Status: design / "why" only — not implemented.** An *experiential* arm: build RAG once end-to-end to learn it, and use the eval harness to test whether retrieval beats the distilled Approach 2 profile. The hypothesis is falsifiable; **"distillation still wins" is an acceptable, documented result, not a failure.** Approach 2 currently scores recall 0.1143 / precision 0.3333 (README → *Eval Execution results*), so there is real headroom worth probing.

**Why now — what changed from the earlier "RAG doesn't fit" conclusion** (see the `rag-not-suitable` memory): the prior call rejected RAG for ~10 repo READMEs — small, query-independent, summarizable wholesale, so distillation strictly dominated. Approach 3 widens the corpus to **~10 blog posts + ~10 READMEs + ~10 white papers**. White papers are the differentiator: long and dense, they (a) don't distill cleanly without dropping the nuance that distinguishes relevant stories, and (b) are too costly to re-send raw on the 4-hourly cadence (re-paying tens of K tokens ~180×/month) while diluting the per-story signal. Retrieving only the relevant slices is RAG's genuine niche — the condition the rejection memo named as "when RAG becomes the right call." It still only *partly* holds (the corpus likely fits Haiku's 200K window), so the win is measured, not assumed.

**Known risk carried over (the retrieval-key problem):** the classifier has no natural per-query key — it builds a *fixed* interest model every run. Using the batch's story titles as the retrieval query biases retrieval toward *confirming* context and can inflate false positives. The eval must watch **FP**, not just recall.

**New dependency (resolved):** Anthropic has no first-party embeddings endpoint, so the vector step uses a third-party embedder — **chosen: Voyage AI** (`voyage-4-lite`). Key in the macOS Keychain as `VOYAGE_API_KEY`; read by the `voyageapi` package.

**Embed step (done):** `go run . embed` (→ `rag.BuildCorpusIndex()`) reads `profile/corpus/` (~10 blogs + ~10 READMEs + ~10 white papers), chunks each file into 800-word windows with 120-word overlap (`rag.ChunkText`), embeds all chunks via `voyageapi.EmbedDocuments`, and writes `profile/corpus_index.json` (git-ignored; provenance metadata + per-chunk `{source, type, chunk_index, text, embedding}`). The corpus and raw sources in `profile/{readmes,blogs,whitepapers}` are git-ignored too (backed up in Obsidian).

**Free-tier rate limit (important):** Voyage's no-payment tier throttles to **3 requests/min and 10K tokens/min** (the 200M free-token allowance still applies, so the ~150K-token corpus is ~free). `voyageapi` paces around this, so a full `embed` run takes **~20 min**. Adding a payment method on the Voyage dashboard lifts the throttle (still free under 200M tokens) and the pacing just becomes harmless overhead.

**Retrieval step (done):** `voyageapi.EmbedQuery` embeds the query with `input_type: "query"` (queries must NOT go through `EmbedDocuments` — Voyage embeds the two retrieval sides differently); `rag/retrieve.go` ranks the index by cosine similarity and returns the top-k chunk texts. `armcontext.BuildProfile`'s arm 2 case joins the batch's ~30 titles as the retrieval query (batch-level, one query per run — per-title retrieval was rejected: 30 paced Voyage calls and the batch prompt pools the context anyway), calls the `loadRagContext` DI var (→ `rag.RetrieveContext`), and returns the chunks on `UserProfile.RetrievedExcerpts`. `UserProfile` gained `RetrievedExcerpts []string` (its designed extension point) so no call-site signatures churned.

> **`RetrievedExcerpts` holds the top-k retrieved for *this* call — not the corpus, not a cached index.** Every element is inlined verbatim into the system prompt by `ragClassificationPrompt`, so the slice's length *is* the per-call token cost; loading the whole `corpus_index.json` into it would re-send ~150K tokens every 4h, which is precisely the cost Approach 3 exists to avoid (that would be long-context stuffing, a different arm). It is also the one field on `UserProfile` with a **per-call** lifetime — `Summary`/`Interests` are built once per run, but the excerpts' retrieval query is the batch's own titles. That is why `armcontext.BuildProfile` takes the batch's `stories` (its retrieval query) and is called **per batch** — production once per run, `evalHarness.classifyInBatches` once per chunk — rather than once up front with the excerpts patched in afterwards. Renamed from `CorpusChunks`, which read as "the corpus chunks" and invited exactly the wrong mental model.

> **Reconciled with issue #11 (explicit arm selection) when this branch merged `main`.** Two things changed from the original design above. (1) There is no longer a *precedence* chain between context sources: `ConstructPromptSystemAttribute` switches on the **arm**, so arm 2 builds from corpus chunks, arm 1 from summary/interests, arm 0 from the static rules, and a profile carrying both chunks and interests still yields exactly its arm's prompt. Arm purity is now enforced by the arm, not by ordering. (2) **The fail-soft chain is gone.** Retrieval only runs when `arm == ArmRAG`, and a retrieval failure (typically: the embed step never ran) **returns an error** so the run exits non-zero. Silently degrading arm 2 → arm 1 → arm 0 is exactly what issue #11 set out to prevent — it would publish numbers labelled "RAG" that were really the static ruleset.

**Eval arm (done):** `go run . eval 2` works. `evalHarness.classifyInBatches` rebuilds the profile **per batch** via `armcontext.BuildProfile`, so retrieval is keyed by that batch's own titles, so each of the 12 batches gets its own top-k chunks — the same retrieval shape production uses, rather than one global context reused across the dataset. A retrieval failure aborts the eval (no fail-soft), for the same reason as the classify path.

> ⚠️ **Voyage pacing gap in the arm 2 eval (known, unfixed).** `EmbedQuery` has 429 retry-with-backoff but — unlike `EmbedDocuments` — applies **no** `VOYAGE_MIN_REQUEST_GAP` pacing between calls, because production only ever makes *one* retrieval call per run. The eval makes **12 back-to-back**, which on the free tier (3 RPM) means calls 4+ get 429'd and self-pace via linear backoff (30s, 60s, …, `VOYAGE_MAX_RETRIES` = 6). It completes, but takes several minutes with wasted round trips. Fix when it becomes annoying: either sleep `VOYAGE_MIN_REQUEST_GAP` between eval retrievals, or move min-gap pacing into `voyageapi` so it applies to queries too. Unaffected if a payment method is on the Voyage account.

**Next step:** eval Approach 3 vs 1 & 2 — add F1 as the cross-arm ranking score, run each arm k times for mean ± stddev, and watch **FP/precision** for the retrieval-key confirmation-bias failure mode. Record results in README → *Eval Execution results*.
