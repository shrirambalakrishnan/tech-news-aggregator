## Requirements

- find the trending HackerNews articles I will be interested to read
- send this list to me every 6 hours
	- write to a file with timestamp for me read

## Roadmap
- `Approach 1` [Completed ✅]
	- classify whether an article is a cs tech blog or not using `Generic "CS Technical" filter`
- `Approach 2` [Completed ✅]
	- Extract user profile from Github Profile
	- Filtering based on the `profile context` extracted from Github repos' readme files
- **eval: raw READMEs in the classify call vs. prebuilt profile extraction** [Completed ✅]
	- The prebuild step distills READMEs into `profile/user_context.json` and the
	  classify run reads that artifact. This is a *lossy proxy*, not a faithful
	  replay of "what Claude would extract if we passed the raw READMEs into the
	  classify call directly" — distillation drops nuance, the extraction prompt
	  imposes a lens, and the LLM is non-deterministic. We accept this trade for
	  cost, stability, and inspectability (see Cost model below).
	- Build an offline eval that runs both paths over a sample of HN front pages
	  and compares the classified sets (precision/recall/overlap), so the fidelity
	  gap is measured rather than assumed. Use it to tune the extraction prompt to
	  capture exactly the signals the classifier relies on.
- `Approach 3` - Use RAG [Completed ✅] — **experiential: does retrieval actually beat distillation here?**
	- **Goal:** learn RAG hands-on *and* measure whether retrieval beats the
	  distilled Approach 2 profile — don't assume it. The question is falsifiable via
	  the eval harness (precision/recall vs Approach 1 & 2). "Distillation still wins"
	  is an acceptable, documented outcome — not a failure.
	- **Why now — the corpus changed.** The earlier "RAG doesn't fit here" call was
	  about ~10 repo READMEs alone: small, query-independent, summarizable wholesale,
	  so distillation strictly dominated. Approach 3 widens the corpus to ~10 personal
	  blog posts + ~10 repo READMEs + ~10 white papers I've read. **White papers are
	  the differentiator:** long and dense, they (a) don't distill cleanly without
	  dropping the nuance that separates a relevant story from noise, and (b) are too
	  costly to re-send raw on the 4-hourly cadence (re-paying tens of K tokens
	  ~180×/month) while diluting the per-story signal. Retrieving only the relevant
	  slices is RAG's genuine niche — the condition the rejection note named as "when
	  RAG becomes the right call." It still only *partly* holds (the corpus likely
	  fits Haiku's 200K window), so the win is measured, not assumed.
	- **Why it might still lose (recorded up front):** the classifier has no natural
	  per-query key — it builds a *fixed* interest model every run. Using the batch's
	  story titles as the retrieval query biases toward *confirming* context, which
	  can inflate false positives. The eval must watch **FP**, not just recall.
	- **New dependency (resolved):** Anthropic has no embeddings endpoint, so RAG needs
	  a third-party embedder. **Chosen: Voyage AI** (Anthropic's recommended partner);
	  its key is stored in the macOS Keychain as `VOYAGE_API_KEY` (see *Secrets &
	  environment* under Setup).
	- **Embed step done:** `go run . embed` chunks `profile/corpus/` (git-ignored; ~10
	  blogs + ~10 READMEs + ~10 white papers) and embeds it via Voyage into
	  `profile/corpus_index.json` (see *Embeddings step* below).
	- **Retrieval step done:** each batch's story titles are embedded as one query,
	  cosine-ranked top-k against the index, and the retrieved chunks are inlined into
	  the classify prompt. Live in **both** the scheduled run (`go run . 2`) and the
	  eval (`go run . eval 2`) — see *Run modes* and *Retrieval step* below.
	- **Eval done:** `go run . eval 2` scores arm 2 against the same hand-labelled
	  dataset as arms 0 and 1, retrieving per batch exactly as production does. Numbers
	  under *Eval Execution results*.
	- **Follow-ups (eval v2, when the comparison needs to be sharper):** add F1 as a
	  single cross-arm ranking score, run each arm k times for mean ± stddev (the LLM is
	  non-deterministic, so one run is a point estimate), and keep watching
	  **FP/precision** for the confirmation-bias failure mode recorded above.

## Run modes (arms)

The classifier flow is selected **explicitly** by an `arm` argument — nothing on
disk decides it. Each arm picks exactly one `UserProfile` initialization and one
classification flow, and errors (non-zero exit) when its required data is absent
rather than silently falling back.

| arm | UserProfile | Flow | Errors if… |
|-----|-------------|------|------------|
| `0` | none | generic/static "technical CS" prompt | never — production default |
| `1` | `summary` + `interests` from `profile/user_context.json` | interests injected into the prompt | the distilled JSON is missing (run `prebuild` first) |
| `2` | top-k corpus excerpts retrieved for the batch being classified | excerpts inlined into the prompt as evidence of the reader's interests | `profile/corpus_index.json` is missing, or retrieval fails (run `embed` first) |

```bash
go run .                # normal run, arm 0 (default; cron-safe)
go run . 1              # normal run, arm 1 (interests) — needs prebuild's JSON
go run . 2              # normal run, arm 2 (RAG) — needs embed's corpus index
go run . eval 0         # eval under arm 0 (arm is REQUIRED for eval)
go run . eval 1         # eval under arm 1
go run . eval 2         # eval under arm 2 — slow on Voyage's free tier, see below
go run . prebuild       # regenerate profile/user_context.json (see below)
go run . embed          # regenerate profile/corpus_index.json (see below)
```

Production defaults to **arm 0** so the unattended `tech-news-run.sh` path stays
cron-safe. That is a deliberate regression from the previous implicit
interests-when-present behaviour; making the arm user-configurable is a future
iteration. `eval` requires an explicit arm (no default) so an eval meant for one
arm can never silently score another.

## Prebuild step (user context)

`go run . prebuild` fetches the configured user's GitHub READMEs (`GITHUB_USERNAME`,
see `.env.example`), asks the LLM to extract an interest profile, and writes it to
`profile/user_context.json` (a regenerable, git-ignored cache with a
`generated_at` timestamp). **Arm 1** reads that file (and errors if it's absent);
arm 0 ignores it. Run prebuild occasionally — **not** on the 4-hourly schedule —
since it is the expensive path.

## Embeddings step (Approach 3 / RAG)

`go run . embed` builds the RAG index used by Approach 3. It reads every file in
`profile/corpus/`, splits each into fixed-size **word windows** (800 words, 120-word
overlap), embeds the chunks with **Voyage AI** (`voyage-4-lite`), and writes
`profile/corpus_index.json` — a git-ignored, regenerable cache holding provenance
metadata plus one record per chunk (`source`, `type`, `chunk_index`, `text`,
`embedding`). Run it occasionally (a one-off, like prebuild — **not** on the 4-hourly
schedule), and re-run it whenever the corpus changes.

It needs `VOYAGE_API_KEY` in the environment (one-off — not exported by
`tech-news-run.sh`):

```bash
export VOYAGE_API_KEY=$(security find-generic-password -a "$USER" -s VOYAGE_API_KEY -w)
go run . embed
```

**Overlap, briefly:** consecutive chunks share their last 120 words so an idea that
straddles a window boundary still appears whole in at least one chunk (same trick as
re-reading a tail of the previous block when a record spans a fixed-size block).

**Free-tier rate limit:** without a payment method, Voyage throttles to **3
requests/min and 10K tokens/min** (the 200M free-token allowance still applies, so the
~150K-token corpus is effectively free). The embedder paces around this — token-bounded
batches, an inter-request delay, and 429 retry-with-backoff — so a full run takes
**~20 min**. Adding a payment method on the Voyage dashboard lifts the throttle (still
free under 200M tokens); the pacing then just becomes harmless overhead.

## Retrieval step (Approach 3 / RAG) — what `arm 2` does at run time

Arm 2 replaces the distilled profile with corpus excerpts retrieved for the
stories being classified right now:

1. **Query.** The batch's ~30 story titles are joined with newlines into a single
   query. One query per batch, not per title — 30 paced Voyage calls would be far
   slower, and the batch prompt pools the excerpts anyway.
2. **Embed.** The query goes through `voyageapi.EmbedQuery`, which sets
   `input_type: "query"`. Queries must **not** go through the document path:
   Voyage embeds the two retrieval sides differently.
3. **Rank.** Cosine similarity against every chunk in `profile/corpus_index.json`;
   the top 5 (`RETRIEVAL_TOP_K`) chunk texts win.
4. **Classify.** Those 5 excerpts are inlined verbatim into the system prompt as
   evidence of the reader's interests, in place of arm 1's summary + interests.

Only the retrieved handful is sent — never the whole index. Inlining all ~150K
corpus tokens every 4 hours is exactly the cost Approach 3 exists to avoid (that
would be long-context stuffing, a different experiment).

**No fail-soft.** A missing index or a failed retrieval **exits non-zero**; arm 2
never quietly degrades into arm 1 or arm 0. Publishing numbers labelled "RAG" that
were really the static ruleset is the failure mode explicit arm selection exists
to prevent. Run `go run . embed` first — and note the index is git-ignored, so a
fresh clone has to rebuild it.

> ⚠️ **`go run . eval 2` is slow on Voyage's free tier.** The eval retrieves once
> per batch — 12 back-to-back queries — but query embedding has no inter-request
> pacing (production only ever makes *one* retrieval call per run, so it never
> needed it). On the 3 RPM free tier, calls 4+ get 429'd and self-pace via linear
> backoff (30s, 60s, …). The run completes and the numbers are unaffected, but
> expect several minutes of waiting. Adding a payment method on the Voyage
> dashboard removes it entirely.

## Cost model: raw READMEs vs. prebuilt context

Why a prebuild step exists instead of just sending the READMEs into every classify
call. The classify job runs every 4 hours (~180 runs/month). Sending raw READMEs
each time re-pays for the same tokens 180×; the prebuild extracts once (e.g. weekly)
and injects only a small (~300-token) profile per classify call.

Estimates use **Claude Haiku 4.5** pricing ($1.00 / 1M input tokens), ~1,500 tokens
per README, and the rule of thumb **1 token ≈ 0.75 words ≈ ~4 characters** for
English prose (markdown/code/URLs run denser, ~3–3.5 chars/token). These are
estimates — only the `count_tokens` endpoint gives exact figures.

| Repos | Words   | Letters (chars) | Tokens  | Raw $/call | Raw $/month (×180) | Prebuild $/month |
|-------|---------|-----------------|---------|------------|--------------------|------------------|
| 2     | ~2,250  | ~12,000         | ~3,000  | $0.003     | $0.54              | ~$0.06           |
| 10    | ~11,250 | ~60,000         | ~15,000 | $0.015     | $2.70              | ~$0.12           |
| 20    | ~22,500 | ~120,000        | ~30,000 | $0.030     | $5.40              | ~$0.17           |
| 30    | ~33,750 | ~180,000        | ~45,000 | $0.045     | $8.10              | ~$0.23           |

Notes:
- The **context window is not the constraint** here — even 30 READMEs (~45K tokens)
  use ~23% of Haiku's 200K window. It would take ~60–100 repos before fit matters.
- **Prompt caching does not rescue the raw approach**: max cache TTL is 1 hour, but
  runs are 4 hours apart, so the cache always expires between runs.
- The real drivers are **cost × run-frequency** and **signal quality** (raw READMEs
  are mostly boilerplate noise), not fitting in the prompt.

## Tech Stack
- Go script
- Claude / Open AI API for LLM Calls

## Setup

### Secrets & environment

The project needs two secret API keys and one non-secret config value:

| Name | Type | Used by | Where it lives |
|------|------|---------|----------------|
| `ANTHROPIC_API_KEY` | secret | `claudeapi` — every Claude call (classify + profile extraction) | macOS Keychain |
| `VOYAGE_API_KEY` | secret | `voyageapi` — embedding the `profile/corpus` chunks (`go run . embed`) and each arm 2 retrieval query | macOS Keychain |
| `GITHUB_USERNAME` | non-secret | `profile` prebuild — whose repo READMEs to fetch | `.env` (see `.env.example`) |

#### API keys (macOS Keychain)

Cron/launchd jobs don't inherit shell environment variables, so secret keys are stored in the macOS Keychain and exported by `tech-news-run.sh` at run time.

**Store the keys (one-time):**
```bash
security add-generic-password -a "$USER" -s "ANTHROPIC_API_KEY" -w "your-anthropic-key-here"
security add-generic-password -a "$USER" -s "VOYAGE_API_KEY"    -w "your-voyage-key-here"
```

**Verify they work:**
```bash
security find-generic-password -a "$USER" -s "ANTHROPIC_API_KEY" -w
security find-generic-password -a "$USER" -s "VOYAGE_API_KEY"    -w
```

> **Note:** `tech-news-run.sh` exports only `ANTHROPIC_API_KEY`, which is all the scheduled arm 0 run needs. `VOYAGE_API_KEY` is required by `go run . embed` **and** by any arm 2 run (each retrieval embeds its query), so export it manually for those — see *Embeddings step*. If you ever switch the scheduled run to arm 2, the runner has to export it too.

## Runner script

`tech-news-run.sh` fetches the key from keychain and runs the Go program. Make it executable:

```bash
chmod +x tech-news-run.sh
```

## Scheduling with launchd (every 4 hours)

Cron on macOS can't access the login keychain, so we use `launchd` instead.

**Install:**
```bash
sed "s|REPO_PATH|$(pwd)|g" com.technews.plist > ~/Library/LaunchAgents/com.technews.plist
launchctl load ~/Library/LaunchAgents/com.technews.plist
```

**Verify it's loaded:**
```bash
launchctl list | grep technews
```

**Check logs:**
```bash
tail -f /tmp/tech-news.log
```

**Uninstall:**
```bash
launchctl unload ~/Library/LaunchAgents/com.technews.plist
rm ~/Library/LaunchAgents/com.technews.plist
```


## Eval Execution results

### Eval 0 - Based on generic prompt

-- Confusion matrix --
  TP (hit, flagged & relevant)        = 14
  FP (false alarm, flagged but dud)   = 120
  TN (correct skip)                   = 186
  FN (miss, skipped but relevant)     = 21

-- Metrics --
  Precision (of flagged, % good)      = 0.1045
  Recall    (of good, % caught)       = 0.4000

### Eval 1 - Based on user github profile context

-- Confusion matrix --
	TP (hit, flagged & relevant)        = 4
	FP (false alarm, flagged but dud)   = 8
	TN (correct skip)                   = 298
	FN (miss, skipped but relevant)     = 31

-- Metrics --
	Precision (of flagged, % good)      = 0.3333
	Recall    (of good, % caught)       = 0.1143

### Eval 2 - Based on interests obtained from user data (using RAG)

================ EVAL REPORT ================

-- Confusion matrix --
  TP (hit, flagged & relevant)        = 14
  FP (false alarm, flagged but dud)   = 70
  TN (correct skip)                   = 236
  FN (miss, skipped but relevant)     = 21

-- Metrics --
  Precision (of flagged, % good)      = 0.1667
  Recall    (of good, % caught)       = 0.4000