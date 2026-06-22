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
- `Approach 3` - Use RAG [Pending 🟠] — **experiential: does retrieval actually beat distillation here?**
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
	- **New dependency:** Anthropic has no embeddings endpoint, so RAG needs a
	  third-party embedder (Voyage AI — Anthropic's recommended partner — or a local
	  sentence-transformers model). Provider choice pending.
	- Evaluate Approach 3 against Approach 1 and Approach 2 with the existing harness.

## Prebuild step (user context)

`go run . prebuild` fetches the configured user's GitHub READMEs (`GITHUB_USERNAME`,
see `.env.example`), asks the LLM to extract an interest profile, and writes it to
`profile/user_context.json` (a regenerable, git-ignored cache with a
`generated_at` timestamp). The normal `go run .` run reads that file. Run prebuild
occasionally — **not** on the 4-hourly schedule — since it is the expensive path.

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

### API Key (macOS Keychain)

Cron jobs don't inherit shell environment variables, so the API key must be stored in macOS Keychain.

**Store the key (one-time):**
```bash
security add-generic-password -a "$USER" -s "ANTHROPIC_API_KEY" -w "your-api-key-here"
```

**Verify it works:**
```bash
security find-generic-password -a "$USER" -s "ANTHROPIC_API_KEY" -w
```

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

### Run1 - based on user github profile context
-- Confusion matrix --
	TP (hit, flagged & relevant)        = 4
	FP (false alarm, flagged but dud)   = 8
	TN (correct skip)                   = 298
	FN (miss, skipped but relevant)     = 31

-- Metrics --
	Precision (of flagged, % good)      = 0.3333
	Recall    (of good, % caught)       = 0.1143

### Run2 - based on generic CS promt

-- Confusion matrix --
  TP (hit, flagged & relevant)        = 14
  FP (false alarm, flagged but dud)   = 120
  TN (correct skip)                   = 186
  FN (miss, skipped but relevant)     = 21

-- Metrics --
  Precision (of flagged, % good)      = 0.1045
  Recall    (of good, % caught)       = 0.4000