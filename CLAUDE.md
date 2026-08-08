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
go run . 4                      # run under arm 4 (RAG per-story + cross-encoder rerank; needs embed's corpus index)
go run . prebuild               # extract GitHub interest profile -> profile/user_context.json (occasional)
go run . embed                  # chunk+embed profile/corpus -> profile/corpus_index.json (requires VOYAGE_API_KEY; ~20 min on Voyage free tier)
go run . eval 0                 # eval under a specific arm (arm is REQUIRED for eval; 0|1|2|3|4); writes a run record to evalRuns/
go run . eval-report            # read evalRuns/ back: every run, per-configuration mean ± spread, then per-story stability (free; no API call, writes nothing)
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
| `VOYAGE_API_KEY` | secret | `voyageapi` (`os.Getenv`) — the `embed` and `calibrate-floor` steps, and every arm 2/3/4 run (arm 4 also calls `/v1/rerank`) | macOS Keychain |
| `GITHUB_USERNAME` | non-secret | `profile/github.go` (`os.Getenv`) — whose READMEs to fetch | `.env` (see `.env.example`) |

For local runs export the keys directly; in production `tech-news-run.sh` fetches secrets from the macOS Keychain (`security find-generic-password -a "$USER" -s <NAME> -w`) and exports them before `go run .`. **The runner exports only `ANTHROPIC_API_KEY`** — `VOYAGE_API_KEY` is needed only by the one-off `go run . embed` step (not the 4-hourly run), so export it manually for that: `export VOYAGE_API_KEY=$(security find-generic-password -a "$USER" -s VOYAGE_API_KEY -w)`. See README.md for the Keychain + launchd install steps.

## Architecture

**Explicit arm selection (issue #11).** The classification flow is chosen by an
explicit `arm` argument, not by what happens to be on disk. `main.parseArm`
validates the CLI arg; `arm 0` = generic/static prompt with no profile (the
production default, kept for cron-safety), `arm 1` = interests injected from the
distilled JSON, `arm 2` = RAG with one query blended from the batch's titles,
`arm 3` = RAG with one query **per story**, pooled, `arm 4` = arm 3's retrieval
with a **cross-encoder reranking** each story's candidates before pooling (all
three retrieve from the embed step's index and are live in **both** the classify
path and `evalHarness`).
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
5. **`voyageapi` package** (Approach 3): thin Voyage AI client, mirroring `claudeapi`, covering two endpoints. **Embeddings** (`POST /v1/embeddings`): `EmbedDocuments([]string) ([][]float32, error)` token-batches inputs and **paces requests for Voyage's no-payment tier** (3 RPM / 10K TPM): token-bounded batches, an inter-request delay, and 429 retry-with-backoff (all tunable package vars). **Rerank** (`POST /v1/rerank`, Approach 6): `Rerank`/`RerankMany` score (query, document) pairs with `VOYAGE_RERANK_MODEL` = `rerank-2.5`; `RerankMany` is arm 4's entrypoint and paces between calls (~52s at 6 chunks) — a cross-encoder scores one query per call, so N stories cost N requests and cannot be batched. Reads `VOYAGE_API_KEY`. Organized **one file per layer**, with the second endpoint spread across the same three rather than forming a parallel stack: `voyage.go` (PUBLIC API: `EmbedDocuments` + the `embedBatch` DI seam), `rerank.go` (PUBLIC API: `Rerank`/`RerankMany` + the `rerankBatch` seam), `ratelimit.go` (POLICY: the config vars, `planBatches`, `pacingDelay`, `retryOn429`, and the `sleep` seam), `client.go` (TRANSPORT: wire types + `embedBatchHTTP`/`rerankHTTP`).

   > **One rate-limit policy covers both endpoints — the throttle is the ACCOUNT's, not the endpoint's.** 3 RPM / 10K TPM was measured on embeddings and confirmed by probe on `/v1/rerank` at the shape arm 4 sends (6 chunks + 1 title), where the paced ~52s behaved as predicted. So there is no parallel `VOYAGE_RERANK_*` constant set: both paths share `VOYAGE_TPM_LIMIT`, `VOYAGE_MIN_REQUEST_GAP`, `VOYAGE_MAX_RETRIES` and `VOYAGE_RETRY_BACKOFF` (60s, linear: 1min, 2min, 3min…), and both call `pacingDelay` and `retryOn429`. The only per-endpoint difference is the **token count** handed to `pacingDelay`: a batch of documents for embeddings, `rerankTokens` (query + its documents) for rerank.
6. **`rag` package** (Approach 3): organized **one file per pipeline stage** — `corpus.go` is the INPUT stage (`CORPUS_DIR`, `readCorpusFiles`, `inferType`; types are stamped at read time so later stages never inspect filenames); `chunk.go` is the TRANSFORM stage (pure `ChunkText(text, window, overlap)`, fixed-size word windows, 800/120); `index.go` is the OUTPUT stage (the `CorpusIndex` artifact: provenance metadata + `[]Chunk`, with `Write`/`LoadCorpusIndex`); `build.go` is the PIPELINE (the `embedDocuments` DI seam, `buildIndex`, and `BuildCorpusIndex()` orchestrating read `profile/corpus/` → chunk → embed → write `profile/corpus_index.json`); `retrieve.go` is the RETRIEVAL stage (pure `cosineSimilarity` + `topKBySimilarity`, and `RetrieveContext(query, k)` = load index → embed query via `voyageapi.EmbedQuery` → top-k chunk texts; `RETRIEVAL_TOP_K` = 5). `retrieve.go` also holds arm 3's per-story path:
`RetrievePooledContext(queries)` embeds all queries in **one** Voyage request
(`voyageapi.EmbedQueries`), takes `RETRIEVAL_TOP_K_PER_STORY` (2) chunks per
story via `topKAboveFloor`, then `poolChunks` = flatten → dedupe keeping each
chunk's best score (CombMAX, keyed on `{Source, ChunkIndex}`) → sort desc →
truncate to `RETRIEVAL_POOL_CAP` (20). The cap is what keeps arm 3 retrieval
rather than long-context stuffing: without it the prompt grows with the batch
size. `RETRIEVAL_SIMILARITY_FLOOR` ships at **0.0 and is inert** — see Approach 4
below. `TopScoresPerQuery(queries, k)` exposes the raw scores for calibration
without exporting `cosineSimilarity`. `rerank.go` is arm 4's retrieval stage:
`RetrieveRerankedContext(queries)` runs the same per-story cosine selection,
widened to `RERANK_CANDIDATES_PER_STORY` (6) at the permissive
`RERANK_CANDIDATE_FLOOR` (0.0, **inert** — see Approach 6), hands each story's
candidates to the cross-encoder via the `rerankQueries` DI seam, keeps
`RERANK_TOP_K_PER_STORY` (2) by relevance score, and pools with the *unchanged*
`poolChunks`. Wired as the **embed step**: `go run . embed`. Index is git-ignored (regenerable, like `user_context.json`), but under explicit arm selection a **missing index is fatal for arm 2** rather than fail-soft — see Approach 3 below. **Retrieval is wired into both the classify path and the eval arm.**

7. **`armcontext` package**: the single place that answers "what does this arm classify against?". `BuildProfile(arm, stories)` switches on the arm — empty profile for arm 0, the distilled summary/interests for arm 1, the excerpts `rag.RetrieveContext` returns for arm 2, the pooled excerpts `rag.RetrievePooledContext` returns for arm 3, the reranked pool `rag.RetrieveRerankedContext` returns for arm 4 — and errors (never a degraded profile) when the arm's artifact is missing. `retrievalQuery` (unexported) owns arm 2's retrieval key: the batch's titles, newline-joined; `retrievalQueries` owns arm 3's: the same titles as a **slice**, one query each. **Arm 4 reuses `retrievalQueries` verbatim** — arms 3 and 4 ask the same questions of the same index and differ only in how the answers are ranked, which is what makes the ranking their single variable. That one-line difference is the arms' *intended* distinction — not their only one, since they also inject different excerpt counts (see Approach 4's confound note). It exists so `main` and `evalHarness` share one implementation instead of two copies that must be kept in sync for the eval's numbers to transfer; both reach it through a `buildProfileForArm` DI var, which is also what their tests swap (per-arm construction is tested once, in `armcontext`).

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

## Eval run records — the eval *pipeline* (issue #26)

The harness answers "how good is the classifier". It did not answer "good under **what**" — which arm, model, commit's tuning constants, and above all which *data*. That context lived in whoever ran it and was gone by the next run, which is why README's results table needs prose footnotes explaining which rows were measured pre- vs post-notes.

Every `go run . eval <arm>` now writes two files, both stemmed `<UTC timestamp>-arm-<N>` (= the `run_id`):

- **`evalRuns/<run_id>.json`** (`evalHarness/runrecord.go`) — `run_id`, `arm`, `git_sha`, `dataset_hash`, `corpus_index_hash`, `model`, `rerank_model`, `metrics{tp,fp,tn,fn,precision,recall}`. `corpus_index_hash` uses `omitempty` and is **absent for arms 0 and 1**, which never load the index — an empty string would read as "used an empty index" rather than "did not use one". `rerank_model` follows the same rule and is **absent for every arm but 4** (`armUsesReranker`), which also keeps arms 0-3 records byte-identical to those already on disk. It shipped *with* arm 4 rather than as a follow-up because records cannot be back-filled: the first time anyone tries `rerank-2.5-lite`, every arm-4 record written without it becomes permanently unattributable — the same shape of gap arm 1's un-hashed profile still has.
- **`evalRuns/<run_id>.csv`** (`evalHarness/predictions.go`) — one row per dataset item: `run_id, story_id, title, label, predicted`. The JSON gives the counts, the CSV the per-story verdicts behind them; comparing two runs is a `join` on `story_id`. `predicted` comes from `predictedPositiveSet`, **shared with `Evaluate`**, so the rows cannot disagree with the matrix they sit beside — a test recomputes the confusion matrix from the CSV alone to hold that.

**Artifact archiving (`evalHarness/artifacts.go`).** The dataset and `profile/corpus_index.json` both decide the numbers, and both are git-ignored and overwritten in place. Each run hashes them and copies them content-addressed to `evalRuns/datasets/<sha256>.json` and `evalRuns/corpus_index/<sha256>.json`, **write-if-absent** — repeat runs over unchanged inputs add nothing, and a changed input is stored *beside* the old one rather than replacing a snapshot an earlier record still points at. This is the automated version of the `cp profile/corpus_index.json profile/corpus_index.pre-notes.json` step Approach 5's run order relies on a human remembering.

- **Hash = plain sha256 of raw file bytes**, so `shasum -a 256` reproduces it and there are no canonicalisation rules to get subtly wrong. Consequence: it covers the index's `generated_at`, so re-embedding an *unchanged* corpus yields a new hash and a second 4.4 MB copy. Accepted — re-embedding is a deliberate ~20-min act, and nothing rewrites the file between eval runs.
- **Archiving runs BEFORE the first Claude call** and aborts on failure. It is free, so failing early costs nothing; failing after classification would leave an unrecordable run already paid for. Same no-fail-soft rule as issue #11 — a number nobody can trace to its data is the problem this exists to solve.
- **`go run . embed` archives the index too** (`evalHarness.ArchiveCorpusIndex`, wired from `main.runEmbed`). Not what makes an index traceable — the eval flow does that — but what makes it traceable *early*: an index built today and first evaluated next week would otherwise be captured only at eval time, with any `embed` in between overwriting it unsnapshotted. `rag` cannot call this itself (`evalHarness` already imports `rag`, so the reverse edge is a cycle), which also keeps `rag` unaware eval tracking exists.
- **`armUsesCorpusIndex` mirrors `armcontext.BuildProfile`'s retrieval cases** and must be updated alongside them. An arm that reads the index but is missing there records no `corpus_index_hash` and publishes numbers whose retrieval side is untraceable. **`armUsesReranker` is the same contract for the cross-encoder** and mirrors `rag.RetrieveRerankedContext`'s callers.

**Ordering: report first, then record.** `RunEval` prints `metrics.Report()` *before* writing, and returns the write error afterwards. A run costs money and ~10 min, so a failed write earns a non-zero exit but must never cost the operator numbers already paid for. This is why `RunEval` returns an `error` instead of calling `log.Fatal` — which also restores `main.go`'s stated "exactly one exit point" design. Both artifacts are written independently (errors joined) and share **one** `run_id` computed at the top of `RunEval`: two independent timestamps seconds apart can straddle a second boundary and leave a `.json` and `.csv` that no longer look like the same run.

**The record is descriptive, never an input.** Nothing reads it back to change how a run behaves — that is what makes it safe to write after scoring.

> **Known limitations, both deliberate.** (1) **`git_sha` is HEAD, not the working tree** — an uncommitted edit to a tuning constant is invisible, so a record can name a commit whose code is not what ran. Commit tuning changes before measuring them. (2) **Arm 1's `profile/user_context.json` is NOT hashed.** It is equally git-ignored, equally regenerable, and equally decides arm 1's numbers, so arm 1 records pin the code and model but not the profile actually used — an asymmetry with arms 2/3, left as a follow-up rather than an oversight. (3) `rag.BuildCorpusIndex` fails soft (logs, returns nothing), so `main.runEmbed` cannot tell a successful build from a failed one and will archive whichever index is on disk. Truthful about what the RAG arms would read *now*, but not evidence this run produced it; making `BuildCorpusIndex` return an error would close the gap and was deliberately not bundled in.

## The eval-run aggregator (issue #34)

`go run . eval-report` reads `evalRuns/` back and prints three sections. **Free — no Claude call, no Voyage call, and it writes nothing.** It is a view over the records, never an input to them, which is what keeps it unable to change what a future eval means.

Organized one file per stage, mirroring `rag`: `evalHarness/report_load.go` (INPUT — `loadRunRecords`), `report_predictions.go` (INPUT — `loadPredictionsCSV`), `report_group.go` and `report_stability.go` (TRANSFORM — pure statistics), `report.go` (OUTPUT + the `RunEvalReport` entrypoint, streams injected as `calibrateFloor` does).

- **Output 1 — one row per run**, sorted by `run_id` (lexically chronological by construction): `run_id, arm, git_sha(7), dataset_hash(12), corpus_index_hash(12), model, TP, FP, TN, FN, precision, recall`. Hashes are abbreviated; **the model is not** — it is a grouping key, and truncating it could make two different models look like one configuration. An absent `corpus_index_hash` (arms 0/1) renders `—`, never blank or `0`.
- **Output 2 — one row per configuration**, grouped by `(arm, git_sha, model, rerank_model, dataset_hash, corpus_index_hash)` and sorted by `n` desc then every key field asc. The tiebreak is load-bearing: Go randomises map iteration, so without a total ordering the report is nondeterministic and untestable — which is why `rerank_model` had to join the tiebreak chain, not just the key. It is also **rendered** (Output 2's header, its table, and the stability block header, `—` when absent, via `orAbsent`): adding a field to the key without adding it to the display is its own bug, since two genuinely distinct configurations would then print under identical headers. Model names are never truncated — they are grouping keys. Counts are means; precision/recall carry mean and **sample** stddev (n−1 — the runs are draws from a non-deterministic process, not a complete population).
- **`±` prints only when measured.** `aggregate.StdDevDefined` is what makes "not measured" representable; at n=1 `formatAggregate` prints the bare mean. `± 0.0000` would read as perfect reproducibility, the opposite of what one run says — and n=1 is the common case (3 of the 4 records on disk).
- **Precision/recall are averaged PER RUN**, never recomputed from summed counts. Its test fixture had to be *constructed*: recall's denominator (`TP+FN`) is the dataset's positive count and so is constant across runs on one dataset, and the real records happen to have flagged the same number of stories — so a pooled implementation passes against live data. Only precision's denominator varies.
- **The glob is non-recursive**, deliberately: `evalRuns/datasets/` and `evalRuns/corpus_index/` hold `<sha256>.json` artifacts, and a recursive walk would try to parse a 4.4 MB corpus index as a run record.
- **Unusable records are skipped, not fatal** — a deliberate exception to issue #11's no-fail-soft rule, since one broken file should not deny a report over the other twenty. Mitigated by printing the skip *count* into the table header (`=== Eval runs (3, 1 skipped) ===`), because `n` is the number the variance table hangs on. A JSON object with no `run_id` is skipped too: `encoding/json` ignores unknown fields, so any JSON decodes into a zero `RunRecord` and would appear as a fabricated arm-0 run scoring nothing.

**Non-goals, all confirmed:** no README auto-update, no cross-configuration comparison or ranking, and the aggregator writes no files. *(Reading the per-run CSVs was a non-goal at #34 and was retired by #37 — see below.)*

### Output 3 — per-story stability (issue #37)

Outputs 1 and 2 report **counts**, and counts are anonymous: `"tp": 24` is 24 tally marks, not 24 named stories. So Output 2 can say the arm-2 group averaged 22.5 TP across two runs, but not whether it was the **same** stories both times — and two runs scoring 24 and 21 average to 22.5 whether they agreed on 21 stories and disagreed on 3, or agreed on 12 and disagreed on 21. Identical mean, identical spread, opposite meaning: a stable core with a fixed failure set is a repeatable pattern worth fixing, while a different set each run means the mean is mostly luck. Identity was never stored in the record, so no arithmetic over the counts recovers it — it has to come from the per-run predictions CSV.

For each group with **2 or more runs**, `k` = how many of its runs flagged a given story; `k = 0` → never, `k = n` → always, anything between → sometimes. One header, one table, six numbers:

```
=== Per-story stability — arm 2, 1b4a0be, dataset a59941fa34f7,
     index 3daf2eee4e65, claude-haiku-4-5-20251001 (n=2 runs) ===

                  never     sometimes  always
                  (0 of 2)  (1 of 2)   (2 of 2)
relevant (35)     11        3          21
irrelevant (296)  211       11         74
```

- **The split sizes the remedial work**, which the lump FN cannot. A relevant story no run catches will not be caught by re-running — it needs retrieval, prompt or corpus work. One caught *sometimes* has been caught before, so a sampling fix (pinned temperature, or a verdict combined across runs) might convert it. On the arm-2 group: 21 of 35 relevant stories caught by both runs, 3 flip, 11 missed by both.
- **n=1 groups print nothing**, the same rule as the `±` convention: one run measures no stability, since every story it flagged was flagged by every run, and a block would read as perfect consistency.
- **Blocks follow Output 2's order**, reusing `groupRuns`' slice — the total ordering that makes Output 2 deterministic makes this section deterministic for free.
- **Stories are keyed on `story_id`, collapsing the dataset's 341 rows to 331 distinct stories** (10 ids appear twice). Safe on both columns, not just labels: the duplicates all carry `label 0` and agree, and `writePredictionsCSV` derives `predicted` from `predictedPositiveSet`, a map keyed on `story_id`, so duplicate rows **structurally** cannot disagree about the verdict. The visible consequence is the irrelevant row totalling 296 rather than 306; all 35 relevant stories are distinct, so the relevant side — where the conclusions get drawn — is untouched. Deduping *within* a run is load-bearing rather than tidy: counting a duplicated row twice would push its flag count above `n` and the story would never bucket as "always".
- **CSV columns are located by name, not position**, and only `story_id`/`label`/`predicted` are required — so a later column (adding `objectID`, which would preserve all 341 rows, is its own issue) leaves the reader working instead of shifting it silently onto the wrong field.
- **A group whose CSVs cannot all be read is skipped with a warning**, not fatal — `loadRunRecords`' posture toward an unreadable record. Loading is all-or-nothing per group: a block computed from some of the runs would carry a header saying `n=2` over counts measured across one.
- **`evalRuns/` is git-ignored, so the real numbers cannot be a committed test.** The fixtures reproduce the *shape*; reproducing the numbers above is a manual `go run . eval-report`.

**Parked, deliberately:** no title lists (the CSVs are on disk and greppable), no combining-rule scores (unanimous/union precision and recall), and nothing claiming reliability at n=2 — with two runs "sometimes" can only mean 1 of 2, and "never" also absorbs stories that were merely unlucky twice.

**Limitations inherited from the record and undetectable here:** `git_sha` is HEAD, not the working tree, so two runs sharing a sha may have run different uncommitted code and group as one configuration; and arm 1's `profile/user_context.json` is not hashed, so arm-1 rows group on a key omitting an input that decides their numbers. Both are printed as a footnote under Output 2.

## Approach 3 — RAG experiment (built + evaluated)

**Status: built end-to-end and scored.** Embed, retrieval, the classify path (`go run . 2`) and the eval arm (`go run . eval 2`) all work; the numbers live in README → *Eval Execution results*. An *experiential* arm: build RAG once end-to-end to learn it, and use the eval harness to test whether retrieval beats the distilled Approach 2 profile. The hypothesis is falsifiable; **"distillation still wins" is an acceptable, documented result, not a failure.** Approach 2 currently scores recall 0.1143 / precision 0.3333 (README → *Eval Execution results*), so there is real headroom worth probing.

**Why now — what changed from the earlier "RAG doesn't fit" conclusion** (see the `rag-not-suitable` memory): the prior call rejected RAG for ~10 repo READMEs — small, query-independent, summarizable wholesale, so distillation strictly dominated. Approach 3 widens the corpus to **~10 blog posts + ~10 READMEs + ~10 white papers**. White papers are the differentiator: long and dense, they (a) don't distill cleanly without dropping the nuance that distinguishes relevant stories, and (b) are too costly to re-send raw on the 4-hourly cadence (re-paying tens of K tokens ~180×/month) while diluting the per-story signal. Retrieving only the relevant slices is RAG's genuine niche — the condition the rejection memo named as "when RAG becomes the right call." It still only *partly* holds (the corpus likely fits Haiku's 200K window), so the win is measured, not assumed.

**Known risk carried over (the retrieval-key problem):** the classifier has no natural per-query key — it builds a *fixed* interest model every run. Using the batch's story titles as the retrieval query biases retrieval toward *confirming* context and can inflate false positives. The eval must watch **FP**, not just recall.

**New dependency (resolved):** Anthropic has no first-party embeddings endpoint, so the vector step uses a third-party embedder — **chosen: Voyage AI** (`voyage-4-lite`). Key in the macOS Keychain as `VOYAGE_API_KEY`; read by the `voyageapi` package.

**Embed step (done):** `go run . embed` (→ `rag.BuildCorpusIndex()`) reads `profile/corpus/` (~10 blogs + ~10 READMEs + ~10 white papers, plus the `notes-*.md` reading notes added by issue #22 — see Approach 5), chunks each file into 800-word windows with 120-word overlap (`rag.ChunkText`), embeds all chunks via `voyageapi.EmbedDocuments`, and writes `profile/corpus_index.json` (git-ignored; provenance metadata + per-chunk `{source, type, chunk_index, text, embedding}`). The corpus and raw sources in `profile/{readmes,blogs,whitepapers}` are git-ignored too (backed up in Obsidian).

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

**Floor calibration (`go run . calibrate-floor`, `evalHarness/calibrate_floor.go` + `floor_verdict.go`).** Scores every labelled title against the index and prints distribution + survival tables, then a **verdict** section: AUC/d′ against named reading points, the best floor from a fine sweep over every observed score, the interaction with `RETRIEVAL_POOL_CAP`, and a recommended value with its reason. One Voyage request per 128 titles, no Claude call, $0.00. The verdict section was added *after* the first run, when the tables alone proved insufficient to act on — the numbers had to be re-analysed by hand, leaving the conclusion unreproducible, which defeats the point of measuring instead of guessing.

> **`RETRIEVAL_SIMILARITY_FLOOR` is 0.0 and INERT, not tested.** The best floor available (0.2336) sits *below* the ~0.32 that `RETRIEVAL_POOL_CAP = 20` already enforces by truncation, so no value both fires and helps: below it changes nothing, above it cuts into positives. Arm 3's numbers therefore measure per-story retrieval + pooling with floor-filtering never in effect. **Design lesson: before adding a knob, check whether an existing one already dominates it** — the cap is a relative threshold and was doing the floor's job all along.

**Cost:** ~$0.29 per `eval 3` (~290K input tokens: 20 pooled chunks ≈ 23K tokens per call × 12 calls), against ~$0.08 for arm 2 (5 chunks) and ~$0.016 for arms 0/1. Voyage is $0.00 (12 requests, ~4.6K tokens, free tier), but the eval's 12 back-to-back retrievals hit the 3 RPM limit and self-pace via backoff — several minutes.

**Next levers, in cost order:** smaller chunks (currently 800 words — a ~9-word title averaged against an 800-word window dilutes the signal; re-testing at the retrieval layer via `calibrate-floor` is free), wider corpus coverage (recently-read blogs — done, as reading notes: Approach 5 below, and the largest gain recorded so far), then reranking — evaluated first as a scoring function (AUC on a subsample, no Claude call) before any arm is wired. Note that re-chunking invalidates arm 2's baseline, so both arms must be re-run together to keep the comparison honest.

> ⚠️ **Arm 3's numbers above were measured against the PRE-notes index and are stale.** Approach 5 re-ran `embed` over a corpus that now includes reading notes, and arm 3 reads the same `profile/corpus_index.json`. Any arm 2 vs arm 3 comparison — including the cap-matched de-confounding re-run — has to re-run **both** arms against the current index. The same applies to the `calibrate-floor` AUC 0.659 / d′ 0.635 figures and to `RETRIEVAL_SIMILARITY_FLOOR`'s inertness argument, all of which are properties of the pre-notes corpus.

## Approach 5 — reading notes in the corpus (issue #22; built + evaluated, and it worked)

**What it is.** The corpus covered what the user *built* and *published* (READMEs, blogs) and what they *studied* (white papers), but not what they have been reading **recently**. Issue #22 closes that gap: the user's reading notes — `notes-*.md`, first line the blog's title, then its URL, then the bullets — go into `profile/corpus/` and get re-embedded. Tried on **arm 2 only**.

**Code change is deliberately tiny: no retrieval change at all.** `readCorpusFiles` already reads every regular non-hidden file in `profile/corpus/`, so notes are chunked and embedded with zero wiring; `RETRIEVAL_TOP_K`, `cosineSimilarity`, `topKBySimilarity`, `RetrieveContext` and `ragClassificationPrompt` are untouched. The only production edit is one `note` case in `rag.inferType` (matching both `note-` and `notes-`, ahead of the `.txt` case so a `notes-*.txt` is not typed as a white paper), and even that is cosmetic — `Chunk.Type` is stamped at index-build time and **never read** by retrieval or ranking; it exists for inspecting the index with `jq`. Everything else is data authoring.

**Result: 22 TP / 80 FP / 226 TN / 13 FN → precision 0.2157, recall 0.6286.** Up from arm 2's 0.1667 / 0.4000 pre-notes. Clears the win condition (recall > 0.50 at precision ≥ 0.1667) and hold-ground (TP ≥ 14). **Misses FP < 70 — false alarms rose to 80, and that is recorded as a miss**, though precision rose alongside: flagged went 84 → 102, a net +18 splitting **+8 TP / +10 FP** (44% relevant on the net addition vs 17% across arm 2's pre-notes flagged set; net, not a subset diff — the flagged sets are not nested).

**It is the only recorded delta that clears the noise.** Two repeat `eval 2` runs on the same pre-notes index scored recall 0.2571 and 0.3714 beside the tabled 0.4000 — mean **0.343 ± 0.076**, precision mean **0.148 ± 0.022**, so the tabled baseline is the *best* of three samples and the post-notes run still sits ~3.8 SE above the baseline mean. The spread matches the ~8pp binomial SE at 35 positives; retrieval is deterministic given a fixed index, so the variance is entirely Claude's sampling (`claudeapi.Request` sends **no `temperature` field** → API default 1.0). Pinning it to 0 is the one-line lever if noise ever blocks a decision — deliberately not taken, since it re-bases every recorded number at once. **The notes run is itself n=1; repeating it is the cheapest next measurement (~$0.08).**

> **Displacement is measured; its mechanism is not.** The two note chunks — `note-from-blogs-1.md` (410 words) and `note-from-blogs-2.md` (312), one sub-window chunk each, **2 of the index's 168** — take the top-1 slot for **158 of 341** labelled titles (46%), and the pre-notes leader `readme-tech-news-aggregator.md` drops from **120 to 49**. Heavy displacement, wildly disproportionate to a 1.2% share of the index. But **54% of titles still match a non-note chunk** — "notes always win" is false.
>
> **Why they win is unestablished, and issue #22's prediction that it was chunk length is retracted** (see the correction on that thread). The notes are ~360 words, not the predicted ~50, and they are **not** the index's shortest chunks — a README chunk runs 41 words, a blog chunk 51; notes are only shorter than the whitepaper bulk (126 of 168 chunks, mean 783). Length explains little on its own: pre-notes, a 445-word README already held 35% of top-1 slots by itself. Topical overlap with an AI-heavy title stream is at least as plausible. Nothing here separates the two, and separating them means re-chunking, which invalidates every other column in the results table — same shape as the arm 2 vs arm 3 excerpt-volume confound, recorded and not resolved.
>
> ⚠️ These are **top-1** figures (`best_source` in the calibration CSVs) while arm 2 injects **top-5**; the top-5 composition is unmeasured and needs the deferred displacement tooling — which is also what produced these tallies, on the closed PR #23 branch.
>
> **Design lesson: a pre-run prediction about mechanism is a hypothesis, not a caveat.** This one was written into three documents as a stated property of the change before anyone counted a word.

> **The arm 2 prompt is deliberately left stale.** `ragClassificationPrompt` still tells Claude the excerpts are "repository READMEs, blog posts, and white papers"; notes are not named. Adding them would move prompt *wording* at the same time as the corpus, which is exactly the confound Approach 4 got caught by. The recorded result above was produced with this wording. Whether naming notes helps is its own experiment.

**Run order (the index is a single unversioned file, so `embed` destroys the pre-notes state — snapshot first).** *Issue #26 now archives the index automatically at both `embed` and `eval` time (`evalRuns/corpus_index/<sha256>.json`), so the manual `cp` below is no longer the only copy — but it stays the documented procedure here, since the archive is keyed by hash rather than by a name like "pre-notes" and the restore step still needs a file at `profile/corpus_index.json`.* `cp profile/corpus_index.json profile/corpus_index.pre-notes.json` → `calibrate-floor > profile/scores-pre-notes.csv` ($0.00) → `eval 2` (fresh baseline, same session, ~$0.08) → add `notes-*.md` → `embed` (~20 min, $0.00) → `calibrate-floor > profile/scores-notes.csv` (free ranking check before spending) → `eval 2` (~$0.08). Restore with the reverse `cp`. Both `profile/corpus_index*.json` and `profile/scores*.csv` are git-ignored. Verify the notes landed with `jq '[.chunks[] | select(.type=="note")] | length' profile/corpus_index.json`.

**Deferred out of this issue, deliberately (PR #23 was closed for carrying them):** the displacement-measurement tooling (`rag.TopSourcesPerQuery`, a `best_source` CSV column, a top-1-match-by-corpus-file table in `calibrate-floor`) and the `RETRIEVAL_SIMILARITY_FLOOR` 0.0 → 0.3330 change. The floor is arm-3-only (`RetrievePooledContext`) and cannot move arm 2's numbers, so it had no business riding along. Each gets its own issue. **Design lesson: an experiment's tooling and its result are separable changes; bundling them makes the result hostage to reviewing the tooling.**

## Approach 6 — reranking on top of RAG (issue #27; built, NOT yet measured)

**Status: built end-to-end, no eval run yet.** Arm 4 = arm 3's retrieval with a
cross-encoder (`voyageapi.Rerank`, `rerank-2.5`) rescoring each story's
candidates before pooling. Same corpus, same index, same queries
(`retrievalQueries`, shared verbatim with arm 3), same `poolChunks`, same
`RETRIEVAL_POOL_CAP`, same `ragClassificationPrompt`. **No results row exists
until `go run . eval 4` is run** (~5h, ~$0.29 Claude, $0.00 Voyage).

**Why a second ranking stage.** A chunk's embedding is computed at embed time,
before any story exists, and compresses ~800 words into one point — a topical
average, so relevance living in one paragraph out of eight is averaged down by
the other seven. A cross-encoder reads the title and the chunk together in one
forward pass, so nothing is averaged and nothing is precomputed. That is also why
it *follows* cosine rather than replacing it: it emits no reusable vector, so it
must run live per pair, and scoring all 168 chunks per story would cost 168 pairs
instead of 6. Cosine narrows, the cross-encoder reorders.

### The five decisions taken while planning (three of them changed the spec)

1. **`rerank-2.5`, not "rerank2.5".** The spec's id is not one Voyage accepts.
   Valid: `rerank-2.5`, `rerank-2.5-lite`, `rerank-2`, `rerank-2-lite`,
   `rerank-1`, `rerank-lite-1`. Response is `data[].{index, relevance_score}`,
   **already sorted descending**; `index` addresses the *submitted* documents,
   which is the only thing tying a score back to its chunk.
2. **Pacing ships at the spec's ~52s, and the throttle behind it is measured.**
   3 RPM / 10K TPM was probed against `/v1/rerank` at arm 4's own request shape
   (6 chunks + 1 title) and behaved as predicted, so the rerank path reuses the
   embeddings constants rather than getting a parallel set — same limits, same
   confidence, one place to change when the account tier does. (An earlier draft
   of this section called the rerank limits "assumed, not measured" and gave that
   as the reason for separate constants. Both halves were wrong: the probe had
   already been run, and the 30s embeddings backoff it contrasted against was
   itself a bug, now 60s like the rest.)
3. **The rerank score REORDERS; it does not filter.** The issue said "the actual
   filtering is expected to be done by the cross encoder score", but the design
   as specified keeps the top `RERANK_TOP_K_PER_STORY` (2) per story
   *unconditionally* and caps the pool afterwards — nothing is dropped for
   scoring low. Shipping reorder-only keeps arm 4 a single-variable change and
   matches the `calibrate-floor` lesson (measure before adding a knob). Every
   kept `(story, chunk)` pair's cosine **and** rerank score is logged CSV-shaped
   (`rag: rerank-score,<query>,<source>,<chunk_index>,<cosine>,<rerank>`), so a
   rerank floor can be calibrated from the arm-4 run itself instead of a second
   paid one. Logging rather than writing a file keeps `rag` free of new
   artifacts — it writes the index and nothing else.
4. **Baseline is the recorded NO-FLOOR arm 3 row (19/76/230/16) — a choice, not a
   default.** At HEAD `RETRIEVAL_SIMILARITY_FLOOR = 0.3330`, so arm 3 runs at
   **7.1 excerpts/call**; arm 4 (candidate floor 0) should land near arm 3's
   no-floor **18.9**, volume-matched by construction. Comparing against arm 3 *at
   HEAD* would be the Approach 4 excerpt-volume confound again. The cost of this
   choice: the comparison is cross-commit and n=1 on both sides. The alternative
   — re-running arm 3 volume-matched — is worth paying for only if arm 4 lands
   near the acceptance line, and if taken **the floor change must be committed
   first** (`git_sha` is HEAD, not the working tree). **Arm 4 logs its pooled
   count per call**, and it must be checked against 18.9 before the comparison is
   read in either direction: README records twice that assuming a pool size
   instead of counting it produced a wrong conclusion.
5. **The acceptance criteria are cleared by an exact tie.** FP ≤ 76, precision ≥
   0.2000, recall ≥ 0.5429 *are* the no-floor arm 3 row, so landing on the line
   means "did not lose ground", not "beat arm 3". Left as specified, flagged so
   the result is read correctly. At 35 positives the SE on recall is ~8pp.

### Constants, and what is inert

`RERANK_CANDIDATES_PER_STORY` = 6 (count handed to the cross-encoder; bounded by
the probed 10K TPM — 6 × ~800-word chunks ≈ 7.9K tokens ≈ 79% of the minute, so
raising it raises the pacing delay roughly linearly).
`RERANK_TOP_K_PER_STORY` = 2, deliberately equal to `RETRIEVAL_TOP_K_PER_STORY`
so the pooled volume stays comparable to arm 3's.

> ⚠️ **`RERANK_CANDIDATE_FLOOR` = 0.0 is INERT, and is documented as inert rather
> than as "0.0 by design".** Candidates are selected by **rank**, not by score, so
> no value below the 6th-ranked cosine can fire. It is a **separate var from
> `RETRIEVAL_SIMILARITY_FLOOR`, not a reuse**, and the reason is stronger than
> avoiding coupling: the two are opposite in intent. Arm 3's floor is a
> *relevance* filter — the last decision about what reaches the prompt, so it
> wants to be strict. This one is a *recall* filter feeding the cross-encoder, so
> it wants to be permissive enough that nothing the reranker could rescue is
> discarded first. One constant cannot serve both. Arm 3's 0.3330 is untouched,
> so arm 3's recorded numbers stand. `RETRIEVAL_SIMILARITY_FLOOR` shipped inert
> once already and CLAUDE.md records what that cost — a knob that looks tuned but
> cannot move invites someone to tune it and wonder why nothing changed.

**What the pooled score now means.** `poolChunks` is unchanged, so dedupe still
keeps each chunk's best score (CombMAX) and the sort is still descending — but
the score is now a cross-encoder relevance score, produced per `(query,
document)` pair and only loosely comparable **across** queries. Cosine already
carried that caveat; this is a sharper version of it, since nothing normalizes a
cross-encoder's outputs between queries.

**`rerank_model` is on the run record** (issue #27, not deferred) — see the
eval-pipeline section. Arm 1's `profile/user_context.json` is still un-hashed, so
that asymmetry narrows here without closing.

### Out of scope, deliberately

- **Any floor on the rerank *score*.** Reorder-only ships (decision 3). The score
  dump makes one calibratable later from the same run, and `RETRIEVAL_POOL_CAP` =
  20 with `RERANK_TOP_K_PER_STORY` = 2 already truncates — so check whether an
  existing knob already dominates it before adding one.
- **Eval-report presentation** beyond the one key field: no new columns, no
  cross-arm ranking, no README auto-update. PR #23's category.
- **A rerank result cache** keyed on `(index hash, query, chunk key)`. Reranking
  is deterministic given a fixed index, so a cache would make the repeat runs
  needed to clear the noise floor cost only the Claude calls instead of ~5h each.
  Tempting at 5h/run, but it is tooling and a separate bet.
