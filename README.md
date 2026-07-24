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
- **eval: raw READMEs in the classify call vs. prebuilt profile extraction** [Pending 🟠]
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
- `Approach 3` - Use RAG [Pending 🟠]
	- Use RAG to set the context
	- We can make use of full README contents of all repos
	- Evaluate how this Approach 3 performs against Approach 1 and Approach 2

## Run modes (arms)

The classifier flow is selected **explicitly** by an `arm` argument — nothing on
disk decides it. Each arm picks exactly one `UserProfile` initialization and one
classification flow, and errors (non-zero exit) when its required data is absent
rather than silently falling back.

| arm | UserProfile | Flow | Errors if… |
|-----|-------------|------|------------|
| `0` | none | generic/static "technical CS" prompt | never — production default |
| `1` | `summary` + `interests` from `profile/user_context.json` | interests injected into the prompt | the distilled JSON is missing (run `prebuild` first) |
| `2` | corpus chunks *(future)* | RAG embedding flow *(future)* | always — **stubbed**, not implemented this iteration |

```bash
go run .                # normal run, arm 0 (default; cron-safe)
go run . 1              # normal run, arm 1 (interests) — needs prebuild's JSON
go run . eval 0         # eval under arm 0 (arm is REQUIRED for eval)
go run . eval 1         # eval under arm 1
go run . prebuild       # regenerate profile/user_context.json (see below)
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