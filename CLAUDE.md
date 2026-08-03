# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Go CLI that fetches HackerNews front-page stories, uses the Claude API to classify which ones are "technical computer science" stories worth reading, and appends the matches (as Markdown links) to `stories.md` with a timestamp. It is meant to run unattended on macOS via `launchd` every 4 hours.

## Commands

```bash
go run .                        # build + run, arm 0 (requires ANTHROPIC_API_KEY in env)
go run . 1                      # run under arm 1 (interests; needs prebuild's JSON)
go run . 2                      # run under arm 2 (RAG, one blended query; needs embed's corpus index)
go run . 3                      # run under arm 3 (RAG, per-story retrieval; needs embed's corpus index)
go run . prebuild               # extract GitHub interest profile -> profile/user_context.json (occasional)
go run . embed                  # chunk+embed profile/corpus -> profile/corpus_index.json (requires VOYAGE_API_KEY; ~20 min on Voyage free tier)
go run . eval 0                 # eval under a specific arm (arm is REQUIRED for eval; 0|1|2|3)
go run . calibrate-floor        # measure rag.RETRIEVAL_SIMILARITY_FLOOR from the labelled set (free; requires VOYAGE_API_KEY)
go test ./...                   # run all tests
go test ./hackernews_classifier/ -run TestClassifyTechNewsStory   # single test, single package
./tech-news-run.sh              # production entrypoint: pulls API key from macOS Keychain, then `go run .` (arm 0)
```

CI (`.github/workflows/test.yml`) runs `go test ./...` on pushes/PRs to `main`. There is no linter configured.

### Secrets & environment

| Name | Type | Read by | Where it lives |
|------|------|---------|----------------|
| `ANTHROPIC_API_KEY` | secret | `claudeapi` (`os.Getenv`) — every Claude call | macOS Keychain |
| `VOYAGE_API_KEY` | secret | `voyageapi` (`os.Getenv`) — the `embed` and `calibrate-floor` steps, and every arm 2/3 run | macOS Keychain |
| `GITHUB_USERNAME` | non-secret | `profile/github.go` (`os.Getenv`) — whose READMEs to fetch | `.env` (see `.env.example`) |

For local runs export the keys directly; in production `tech-news-run.sh` fetches secrets from the macOS Keychain (`security find-generic-password -a "$USER" -s <NAME> -w`) and exports them before `go run .`. **The runner exports only `ANTHROPIC_API_KEY`** — `VOYAGE_API_KEY` is needed only by the one-off `go run . embed` step (not the 4-hourly run), so export it manually for that: `export VOYAGE_API_KEY=$(security find-generic-password -a "$USER" -s VOYAGE_API_KEY -w)`. See README.md for the Keychain + launchd install steps.

## Architecture

**Explicit arm selection (issue #11).** The classification flow is chosen by an
explicit `arm` argument, not by what happens to be on disk. `main.parseArm`
validates the CLI arg; `arm 0` = generic/static prompt with no profile (the
production default, kept for cron-safety), `arm 1` = interests injected from the
distilled JSON, `arm 2` = RAG with one query blended from the batch's titles,
`arm 3` = RAG with one query **per story**, pooled (both retrieve from the embed
step's index and are live in **both** the classify path and `evalHarness`).
Arms 2 and 3 share the same corpus, index and prompt template, so an eval delta
between them cannot come from prompt **wording**. They were *intended* to differ
in excerpt **selection** only, but they also differ in excerpt **count** (arm 2
injects `rag.RETRIEVAL_TOP_K` = 5, arm 3 up to `rag.RETRIEVAL_POOL_CAP` = 20) —
a live confound, see Approach 4 below. Every arm's context
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
6. **`rag` package** (Approach 3): organized **one file per pipeline stage** — `corpus.go` is the INPUT stage (`CORPUS_DIR`, `readCorpusFiles`, `inferType`; types are stamped at read time so later stages never inspect filenames); `chunk.go` is the TRANSFORM stage (pure `ChunkText(text, window, overlap)`, fixed-size word windows, 800/120); `index.go` is the OUTPUT stage (the `CorpusIndex` artifact: provenance metadata + `[]Chunk`, with `Write`/`LoadCorpusIndex`); `build.go` is the PIPELINE (the `embedDocuments` DI seam, `buildIndex`, and `BuildCorpusIndex()` orchestrating read `profile/corpus/` → chunk → embed → write `profile/corpus_index.json`); `retrieve.go` is the RETRIEVAL stage (pure `cosineSimilarity` + `topKBySimilarity`, and `RetrieveContext(query, k)` = load index → embed query via `voyageapi.EmbedQuery` → top-k chunk texts; `RETRIEVAL_TOP_K` = 5). `retrieve.go` also holds arm 3's per-story path:
`RetrievePooledContext(queries)` embeds all queries in **one** Voyage request
(`voyageapi.EmbedQueries`), takes `RETRIEVAL_TOP_K_PER_STORY` (2) chunks per
story via `topKAboveFloor`, then `poolChunks` = flatten → dedupe keeping each
chunk's best score (CombMAX, keyed on `{Source, ChunkIndex}`) → sort desc →
truncate to `RETRIEVAL_POOL_CAP` (20). The cap is what keeps arm 3 retrieval
rather than long-context stuffing: without it the prompt grows with the batch
size. `RETRIEVAL_SIMILARITY_FLOOR` ships at **0.0 and is inert** — see Approach 4
below. `TopScoresPerQuery(queries, k)` exposes the raw scores for calibration
without exporting `cosineSimilarity`. Wired as the **embed step**: `go run . embed`. Index is git-ignored (regenerable, like `user_context.json`), but under explicit arm selection a **missing index is fatal for arm 2** rather than fail-soft — see Approach 3 below. **Retrieval is wired into both the classify path and the eval arm.**

7. **`armcontext` package**: the single place that answers "what does this arm classify against?". `BuildProfile(arm, stories)` switches on the arm — empty profile for arm 0, the distilled summary/interests for arm 1, the excerpts `rag.RetrieveContext` returns for arm 2, the pooled excerpts `rag.RetrievePooledContext` returns for arm 3 — and errors (never a degraded profile) when the arm's artifact is missing. `retrievalQuery` (unexported) owns arm 2's retrieval key: the batch's titles, newline-joined; `retrievalQueries` owns arm 3's: the same titles as a **slice**, one query each. That one-line difference is the arms' *intended* distinction — not their only one, since they also inject different excerpt counts (see Approach 4's confound note). It exists so `main` and `evalHarness` share one implementation instead of two copies that must be kept in sync for the eval's numbers to transfer; both reach it through a `buildProfileForArm` DI var, which is also what their tests swap (per-arm construction is tested once, in `armcontext`).

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
- **Run shape — batch 30 (locked decision):** the eval replays the dataset through the classifier in chunks of `HACKERNEWS_HITS_PER_PAGE` (~30) per Claude call, then aggregates the predicted IDs across chunks before scoring all 341. Rationale: production sends *all* cumulated stories to Claude in **one** call (see Architecture), which with the defaults is ~30 stories — so chunking the eval at 30 makes each Claude call the same size production actually uses, keeping the measured numbers transferable. It also stays well under `claudeapi`'s `MaxTokens: 1024` output cap: sending all 341 at once can truncate the returned JSON ID array, which `json.Unmarshal` then fails to parse → the classifier returns `[]` → recall silently scores 0. **Any** unparseable response fails that way, so `ClassifyTechNewsStory` runs the text through `extractJSONArray` (first `[` … last `]`) before unmarshalling — the prompts forbid markdown fences, but arm 2 inlines fenced corpus excerpts and the model mirrors that formatting back, which is what broke the first arm 2 run. Truncation is still fatal-but-silent; a parse failure logs and scores 0. The LLM is non-deterministic, so a single run is a point estimate; run *k* times for mean ± stddev when the numbers look unstable.
- **Token & cost per eval run** (model is Haiku 4.5 — `claudeapi`'s `ANTHROPIC_MODEL_NAME`; **$1.00 / 1M input, $5.00 / 1M output**). With ~30 stories/call and `ceil(341/30) = 12` calls: ~1,100 input tokens/call (≈300 system + ≈800 message of `Story Id: … , Story Title: …` lines) and ≈50 output tokens/call → **≈13K input + ≈0.6K output ≈ $0.016 per run** (~1.6¢). Batching repeats the system prompt across all 12 calls (~$0.003 of that total vs. a single one-shot call — negligible). A *k*-run variance pass costs ~k×. These are estimates from the README token rule of thumb; only `count_tokens` gives exact figures.

## Approach 3 — RAG experiment (built + evaluated)

**Status: built end-to-end and scored.** Embed, retrieval, the classify path (`go run . 2`) and the eval arm (`go run . eval 2`) all work; the numbers live in README → *Eval Execution results*. An *experiential* arm: build RAG once end-to-end to learn it, and use the eval harness to test whether retrieval beats the distilled Approach 2 profile. The hypothesis is falsifiable; **"distillation still wins" is an acceptable, documented result, not a failure.** Approach 2 currently scores recall 0.1143 / precision 0.3333 (README → *Eval Execution results*), so there is real headroom worth probing.

**Why now — what changed from the earlier "RAG doesn't fit" conclusion** (see the `rag-not-suitable` memory): the prior call rejected RAG for ~10 repo READMEs — small, query-independent, summarizable wholesale, so distillation strictly dominated. Approach 3 widens the corpus to **~10 blog posts + ~10 READMEs + ~10 white papers**. White papers are the differentiator: long and dense, they (a) don't distill cleanly without dropping the nuance that distinguishes relevant stories, and (b) are too costly to re-send raw on the 4-hourly cadence (re-paying tens of K tokens ~180×/month) while diluting the per-story signal. Retrieving only the relevant slices is RAG's genuine niche — the condition the rejection memo named as "when RAG becomes the right call." It still only *partly* holds (the corpus likely fits Haiku's 200K window), so the win is measured, not assumed.

**Known risk carried over (the retrieval-key problem):** the classifier has no natural per-query key — it builds a *fixed* interest model every run. Using the batch's story titles as the retrieval query biases retrieval toward *confirming* context and can inflate false positives. The eval must watch **FP**, not just recall.

**New dependency (resolved):** Anthropic has no first-party embeddings endpoint, so the vector step uses a third-party embedder — **chosen: Voyage AI** (`voyage-4-lite`). Key in the macOS Keychain as `VOYAGE_API_KEY`; read by the `voyageapi` package.

**Embed step (done):** `go run . embed` (→ `rag.BuildCorpusIndex()`) reads `profile/corpus/` (~10 blogs + ~10 READMEs + ~10 white papers, plus the `notes-*.md` reading notes added by issue #22), chunks each file into 800-word windows with 120-word overlap (`rag.ChunkText`), embeds all chunks via `voyageapi.EmbedDocuments`, and writes `profile/corpus_index.json` (git-ignored; provenance metadata + per-chunk `{source, type, chunk_index, text, embedding}`). The corpus and raw sources in `profile/{readmes,blogs,whitepapers}` are git-ignored too (backed up in Obsidian).

**Free-tier rate limit (important):** Voyage's no-payment tier throttles to **3 requests/min and 10K tokens/min** (the 200M free-token allowance still applies, so the ~150K-token corpus is ~free). `voyageapi` paces around this, so a full `embed` run takes **~20 min**. Adding a payment method on the Voyage dashboard lifts the throttle (still free under 200M tokens) and the pacing just becomes harmless overhead.

**Retrieval step (done):** `voyageapi.EmbedQuery` embeds the query with `input_type: "query"` (queries must NOT go through `EmbedDocuments` — Voyage embeds the two retrieval sides differently); `rag/retrieve.go` ranks the index by cosine similarity and returns the top-k chunk texts. `armcontext.BuildProfile`'s arm 2 case joins the batch's ~30 titles as the retrieval query (batch-level, one query per run — per-title retrieval was rejected: 30 paced Voyage calls and the batch prompt pools the context anyway), calls the `loadRagContext` DI var (→ `rag.RetrieveContext`), and returns the chunks on `UserProfile.RetrievedExcerpts`. `UserProfile` gained `RetrievedExcerpts []string` (its designed extension point) so no call-site signatures churned.

> **`RetrievedExcerpts` holds the top-k retrieved for *this* call — not the corpus, not a cached index.** Every element is inlined verbatim into the system prompt by `ragClassificationPrompt`, so the slice's length *is* the per-call token cost; loading the whole `corpus_index.json` into it would re-send ~150K tokens every 4h, which is precisely the cost Approach 3 exists to avoid (that would be long-context stuffing, a different arm). It is also the one field on `UserProfile` with a **per-call** lifetime — `Summary`/`Interests` are built once per run, but the excerpts' retrieval query is the batch's own titles. That is why `armcontext.BuildProfile` takes the batch's `stories` (its retrieval query) and is called **per batch** — production once per run, `evalHarness.classifyInBatches` once per chunk — rather than once up front with the excerpts patched in afterwards. Renamed from `CorpusChunks`, which read as "the corpus chunks" and invited exactly the wrong mental model.

> **Reconciled with issue #11 (explicit arm selection) when this branch merged `main`.** Two things changed from the original design above. (1) There is no longer a *precedence* chain between context sources: `ConstructPromptSystemAttribute` switches on the **arm**, so arm 2 builds from corpus chunks, arm 1 from summary/interests, arm 0 from the static rules, and a profile carrying both chunks and interests still yields exactly its arm's prompt. Arm purity is now enforced by the arm, not by ordering. (2) **The fail-soft chain is gone.** Retrieval only runs when `arm == ArmRAG`, and a retrieval failure (typically: the embed step never ran) **returns an error** so the run exits non-zero. Silently degrading arm 2 → arm 1 → arm 0 is exactly what issue #11 set out to prevent — it would publish numbers labelled "RAG" that were really the static ruleset.

**Eval arm (done):** `go run . eval 2` works. `evalHarness.classifyInBatches` rebuilds the profile **per batch** via `armcontext.BuildProfile`, so retrieval is keyed by that batch's own titles, so each of the 12 batches gets its own top-k chunks — the same retrieval shape production uses, rather than one global context reused across the dataset. A retrieval failure aborts the eval (no fail-soft), for the same reason as the classify path.

> ⚠️ **Voyage pacing gap in the arm 2 eval (known, unfixed).** `EmbedQuery` has 429 retry-with-backoff but — unlike `EmbedDocuments` — applies **no** `VOYAGE_MIN_REQUEST_GAP` pacing between calls, because production only ever makes *one* retrieval call per run. The eval makes **12 back-to-back**, which on the free tier (3 RPM) means calls 4+ get 429'd and self-pace via linear backoff (30s, 60s, …, `VOYAGE_MAX_RETRIES` = 6). It completes, but takes several minutes with wasted round trips. Fix when it becomes annoying: either sleep `VOYAGE_MIN_REQUEST_GAP` between eval retrievals, or move min-gap pacing into `voyageapi` so it applies to queries too. Unaffected if a payment method is on the Voyage account.

**Follow-ups (eval v2):** the cross-arm comparison is still one run per arm. Sharpen it when a ranking decision depends on it — add F1 as one cross-arm score, run each arm k times for mean ± stddev (the LLM is non-deterministic), and keep watching **FP/precision** for the retrieval-key confirmation-bias failure mode.

## Approach 4 — per-story RAG retrieval (built + evaluated; did not beat Approach 3)

**Status: built, scored, and the hypothesis was rejected — but the comparison is confounded.** Arm 3 retrieves per story and pools instead of blending the batch into one query, against the same corpus, index, `ragClassificationPrompt` and one-Claude-call-per-batch shape. `ConstructPromptSystemAttribute` shares one branch (`case ArmRAG, ArmRAGPerStory:`) and a test asserts the two prompts are byte-identical *given the same excerpts*, so prompt **wording** cannot explain any delta.

> ⚠️ **It is NOT the single-variable change it was designed to be.** Arm 2 injects exactly `rag.RETRIEVAL_TOP_K` = 5 excerpts; arm 3 injects **up to** `rag.RETRIEVAL_POOL_CAP` = 20. Excerpt **volume** changed alongside excerpt **selection**, so arm 3's larger prompt — up to 20 × ~800-word chunks inlined verbatim — is an alternative explanation for its lower recall (dilution / position effects in long stuffed contexts), and the recorded delta cannot separate the two. **Do not cite the ~$0.29 / ~290K-token cost as evidence the cap bound: that figure was computed *from* the assumption of 20 chunks/call, so it is circular** — and `simulatePoolCap`'s binding check cannot return false at the shipped constants (30 × 2 = 60 raw scores always exceed a cap of 20). The pool's real deduped size in the scored run is **unmeasured**, and that is the actual state of knowledge: "up to 20" includes 5, so a pool that ran near 5 would mean no volume gap and an attributable delta after all. The confound is unresolved, not established — which is exactly why the recorded delta cannot be read in either direction. The byte-identical-prompt test does not catch this: it passes one fixed `UserProfile` to both arms, so it constrains the template, not the content. `rag.TestRetrievalArmsInjectDifferentExcerptVolumes` pins the volume gap at the shipped constants and fails if they are ever equalized without re-running. **De-confound by setting `RETRIEVAL_POOL_CAP` = `RETRIEVAL_TOP_K` and re-running `eval 2` and `eval 3` together** (~$0.08 each — arm 3's cost is dominated by excerpt count). **Design lesson: "same prompt template" is not "same prompt"; a single-variable claim has to account for context volume, not just context wording.**

**Result: 10 TP / 63 FP / 25 FN → precision 0.1370, recall 0.2857.** Both acceptance targets from issue #19 (FP ≤ 35, recall ≥ 0.40) missed. Arm 3 flagged 73 stories vs arm 2's 84; of the 11 it stopped flagging, 4 were relevant — a 36% hit rate among the dropped, against 17% across arm 2's flagged set, i.e. it pruned the *better* part of the set. **But the gap is inside the noise:** at 35 positives the standard error on recall is ~8pp and the difference is ~11pp (~1.4 SE). The defensible claim is "arm 3 is not better", not "arm 3 is worse".

**Why, diagnosed before the eval rather than after.** `calibrate-floor` measured **AUC 0.659, d′ 0.635** over the labelled set: relevant and irrelevant stories' best-chunk scores overlap heavily (class overlap, *not* the embedding cone effect — the range 0.09–0.51 is wide, not compressed). Both arms select from that same weak ranking; no selection strategy rescues a ranking that barely separates the classes. This is the one conclusion the excerpt-volume confound above does **not** touch — it is measured at the retrieval layer, before any prompt is built. **Transferable lesson: measure retrieval ranking quality (free, no Claude call) before building selection strategies on top of it.**

**Floor calibration (`go run . calibrate-floor`, `evalHarness/calibrate_floor.go` + `floor_verdict.go`).** Scores every labelled title against the index and prints distribution + survival tables, a **top-1-match-by-corpus-file** table, then a **verdict** section: AUC/d′ against named reading points, the best floor from a fine sweep over every observed score, the interaction with `RETRIEVAL_POOL_CAP`, and a recommended value with its reason. Two Voyage requests per 128 titles (scores and top-1 sources are separate measurement calls over the same titles), no Claude call, $0.00. The top-source table and the CSV's `best_source` column come from `rag.TopSourcesPerQuery` (issue #22) and exist to make "did a corpus change displace the old winners?" a **repeatable** check — run `calibrate-floor` before and after and diff the two tables, instead of asserting displacement from the score deltas, which cannot show it. Like `TopScoresPerQuery` it returns one projected field (the `Source` filename) and never chunk text, so a measurement cannot quietly become a retrieval. The verdict section was added *after* the first run, when the tables alone proved insufficient to act on — the numbers had to be re-analysed by hand, leaving the conclusion unreproducible, which defeats the point of measuring instead of guessing.

> **`RETRIEVAL_SIMILARITY_FLOOR` is 0.0 and INERT, not tested.** The best floor available (0.2336) sits *below* the ~0.32 that `RETRIEVAL_POOL_CAP = 20` already enforces by truncation, so no value both fires and helps: below it changes nothing, above it cuts into positives. Arm 3's numbers therefore measure per-story retrieval + pooling with floor-filtering never in effect. **Design lesson: before adding a knob, check whether an existing one already dominates it** — the cap is a relative threshold and was doing the floor's job all along.

**Cost:** ~$0.29 per `eval 3` (~290K input tokens: 20 pooled chunks ≈ 23K tokens per call × 12 calls), against ~$0.08 for arm 2 (5 chunks) and ~$0.016 for arms 0/1. Voyage is $0.00 (12 requests, ~4.6K tokens, free tier), but the eval's 12 back-to-back retrievals hit the 3 RPM limit and self-pace via backoff — several minutes.

**Next levers, in cost order:** smaller chunks (currently 800 words — a ~9-word title averaged against an 800-word window dilutes the signal; re-testing at the retrieval layer via `calibrate-floor` is free), wider corpus coverage (recently-read blogs — now being tried as reading notes, Approach 5 below), then reranking — evaluated first as a scoring function (AUC on a subsample, no Claude call) before any arm is wired. Note that re-chunking invalidates arm 2's baseline, so both arms must be re-run together to keep the comparison honest.

## Approach 5 — reading notes in the corpus (issue #22; code landed, measurement pending)

**What it is.** The corpus covers what the user *built* and *published* (READMEs, blogs) and what they *studied* (white papers), but not what they have been reading **recently**. Issue #22 closes that gap by dropping the user's reading notes — `notes-*.md`, first line the blog's real title, then its URL, then the bullets — into `profile/corpus/` and re-embedding. Tried on **arm 2 only**.

**Code change is deliberately tiny: no retrieval change at all.** `readCorpusFiles` already reads every regular non-hidden file in `profile/corpus/`, so notes are chunked and embedded with zero wiring; `RETRIEVAL_TOP_K`, `cosineSimilarity`, `topKBySimilarity`, `RetrieveContext` and `ragClassificationPrompt` are untouched. The only production edit is one `note` case in `rag.inferType` (matching both `note-` and `notes-`), and even that is cosmetic — `Chunk.Type` is stamped at index-build time and **never read** by retrieval or ranking; it exists for inspecting the index with `jq`. Everything else in the issue is data authoring and measurement.

> **Not purely "more corpus".** A note is ~50 words against `CHUNK_WINDOW_WORDS` = 800, so each note becomes a single short chunk, and short chunks tend to score higher against short titles. Notes are therefore expected to take a disproportionate share of the top-5 — that *is* the intended displacement mechanism, but it means the change is "more corpus **and** shorter chunks", and the result has to be read with that stated next to it.

**Predicted outcome, measured before spending (free, from the existing score CSV).** A regex for `AI|LLM|GPT|agent|…` scores **AUC 0.6713** on the labelled set — *higher* than the full 166-chunk RAG pipeline's **0.6580**. AI-ish titles are 90 of 341 and carry 20 of the 35 positives (22.2% relevant vs 6.0% for the rest), so topic is a real signal and notes about AI/agents should lift ranking somewhat. **But inside the AI group the corpus separates relevant from irrelevant by +0.0005** — nothing. Notes raise relevant and irrelevant AI stories together, so the ranking ceiling is roughly the regex's 0.67 and the pipeline is already there by accident. **A `calibrate-floor` AUC near 0.66 after the notes land is the predicted result, not a failure**, and does not block the eval: the bet is on excerpt *usefulness to Claude*, which only the full eval can see.

> ⚠️ **Concrete precision risk.** 70 of those 90 AI-ish titles are irrelevant. If notes make retrieval a better AI-*topic* detector, arm 2 flags more from a group that is 78% wrong — i.e. **notes could plausibly lower precision**. This is the retrieval-key confirmation-bias failure mode documented under Approach 3, and this is the first concrete evidence it is live. That is why the acceptance criteria below are precision-shaped.

**Acceptance criteria (both sides, so neither can be gamed).** Precision-only targets are passable by flagging 3 stories and hitting 1; recall-only targets are passable by flagging everything.
- Hold ground: recall ≥ 0.40 **and** TP ≥ 14 (arm 2's current position).
- Don't buy it with false alarms: precision ≥ 0.1667 **and** FP < 70 (arm 2's current numbers).
- Win condition: **recall > 0.50 at precision ≥ 0.1667.** With 35 positives a single run wobbles ~±0.04 recall, so a delta under ~0.10 is not distinguishable from luck.

**Run order (the index is a single unversioned file, so `embed` destroys the pre-notes state — snapshot first).**
1. `cp profile/corpus_index.json profile/corpus_index.pre-notes.json`
2. `go run . calibrate-floor > profile/scores-pre-notes.csv` — baseline ranking **and** baseline top-source table ($0.00)
3. `go run . eval 2` — fresh baseline in the same session as the after-run (~$0.08)
4. Add `notes-*.md` to `profile/corpus/`
5. `go run . embed` (~20 min, $0.00)
6. `go run . calibrate-floor > profile/scores-notes.csv` — free ranking + displacement check before spending on the eval
7. `go run . eval 2` — the after-run (~$0.08)

Restore with `cp profile/corpus_index.pre-notes.json profile/corpus_index.json`. `profile/corpus_index*.json` and `profile/scores*.csv` are both git-ignored, so neither the snapshot nor the CSVs get committed.

**Verification once the notes exist:** `go test ./...` green; `jq '[.chunks[] | select(.type=="note")] | length' profile/corpus_index.json` non-zero and total chunks above the current 166; the top-source table shows `readme-tech-news-aggregator.md`'s share of top-1 matches dropping from **86/341**. Record the result either way — README → *Eval - Execution results* and here — including both `calibrate-floor` AUCs and the fact that **arm 3's recorded numbers were measured against the pre-notes index and go stale the moment `embed` re-runs**. A null result closes the issue as measured-and-rejected with the numbers written down; given the ceiling above that is the likelier outcome, and still worth ~$0.16 to find out.
