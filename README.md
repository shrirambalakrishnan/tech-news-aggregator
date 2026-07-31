## Requirements

- Find the trending HackerNews articles users will be interested to read
- Update the filtered list of items every N hours

## Approaches

### Approach 1

#### Solution
- classify whether an article is a cs tech blog or not using `Generic "CS Technical" filter`

#### Sequence Diagram

![Approach 1 - Sequence Diagram](documentations/sequenceDiagrams/tech-new-approach-0.png)

### Approach 2

#### Solution

##### Step 1
- Extract `user interests` from public Github Profile
- Github profile username is required for this

##### Step 2
- Filter news items based on the `user interests` extracted from Github repos' readme files

#### Sequence Diagram

![Approach 2 - Sequence Diagram](documentations/sequenceDiagrams/tech-new-approach-1.png)

#### Notes
- The prebuild step distills READMEs into `profile/user_context.json` and the classify run reads that artifact. This is a *lossy proxy*, not a faithful replay of "what Claude would extract if we passed the raw READMEs into the classify call directly"
— Distillation drops nuance
- We accept this trade for
	- cost
	- stability (?)
	- inspectability (?)

### Approach 3 - Use RAG 

#### Solution

- Make use of all the data available for the user
	- Readmes of repositories created by user
	- Blogs written by user in various platforms (medium, substack)
	- Reasearch papers read by user
- Then extract his interests to retrieve the news items the user will be interested int

##### Step 1
As you can see, the number of resources belonging to user can increase to a huge number. 
- Why not distillation like Approach 2 here as well?
- Why use RAG here?

As distilling all the available data to create a `user interests` can be very lossy here.

Hence we use **RAG (Retrieval Augmented Generation)** to build the user embeddings in the first step.

We use VoyageAI for this purpose of generating embeddings based on the user data

##### Step 2
When the script tries to filter the interested news items
- Use the embeddings to find the correct data to be added to the Claude's prompt
	- This in turn depends on the list of news items returned by Algolia API
- This prompt is expected to rightly classify the news items that user will be interested in

#### Sequence Diagram

![Approach 3 - Sequence Diagram](documentations/sequenceDiagrams/tech-new-approach-2.png)

#### Notes
- New dependency
	- Voyage AI
		- In Step 1 - For creating RAG embeddings
		- In Step 2 - For creating query vector based on news items
- **Why it might still lose (recorded up front):** the classifier has no natural
per-query key — it builds a *fixed* interest model every run. Using the batch's story titles as the retrieval query biases toward *confirming* context, which can inflate false positives. The eval must watch **FP**, not just recall.
	- ==Check and remove==

## Run modes (arms)

- The classifier flow is selected **explicitly** by an `arm` argument.
- Each arm picks exactly one `UserProfile` initialization and one classification flow.
- Each of the classification flow errors when its required data is absent rather than silently falling back.

| arm | UserProfile | Flow | Errors if… |
|-----|-------------|------|------------|
| `0` | none | generic/static "technical CS" prompt | never — production default |
| `1` | `summary` + `interests` from `profile/user_context.json` | interests injected into the prompt | the distilled JSON is missing (run `prebuild` first) |
| `2` | top-k corpus excerpts retrieved for the batch being classified | excerpts inlined into the prompt as evidence of the reader's interests | `profile/corpus_index.json` is missing, or retrieval fails (run `embed` first) |

```bash
# Approach 1
go run .                # normal run, arm 0 (default; cron-safe)
go run . eval 0         # eval under arm 0 (arm is REQUIRED for eval)

# Approach 2
go run . prebuild       # regenerate profile/user_context.json (see below)
go run . 1              # normal run, arm 1 (interests) — needs prebuild's JSON
go run . eval 1         # eval under arm 1

# Approach 3
go run . embed          # regenerate profile/corpus_index.json (see below)
go run . 2              # normal run, arm 2 (RAG) — needs embed's corpus index
go run . eval 2         # eval under arm 2 — slow on Voyage's free tier, see below
```

## Prebuild Steps - Explained

### Approach 2 - Prebuild step (user context)

- Command - `go run . prebuild` 
- Fetches the configured user's GitHub READMEs (`GITHUB_USERNAME`, see `.env.example`)
- Asks the LLM to extract an interest profile
- Writes it to `profile/user_context.json` (a regenerable, git-ignored cache with a
`generated_at` timestamp)
	- **Arm 1** reads that file (and errors if it's absent);	
	- `arm 0` and `arm 2` ignores it. 
- Run prebuild occasionally since it is the expensive path.

### Approach 3 - Embeddings step (RAG)

- Command - `go run . embed` 
- Builds the RAG index used by Approach 3
- It reads every file in `profile/corpus/`
	- splits each into fixed-size **word windows** (800 words, 120-word overlap)
		- **What is this Overlap:** consecutive chunks share their last 120 words so an idea that straddles a window boundary still appears whole in at least one chunk (same trick as re-reading a tail of the previous block when a record spans a fixed-size block).
	- embeds the chunks with **Voyage AI** (`voyage-4-lite`)
		- It needs `VOYAGE_API_KEY` in the environment (one-off — not exported by `tech-news-run.sh`)
			- `export VOYAGE_API_KEY=$(security find-generic-password -a "$USER" -s VOYAGE_API_KEY -w)`
		- **Limitations of using this model:** **Free-tier rate limit:** without a payment method,Voyage throttles to **3 requests/min and 10K tokens/min** (the 200M free-token allowance still applies, so the ~150K-token corpus is effectively free). The embedder paces around this — token-bounded batches, an inter-request delay, and 429 retry-with-backoff — so a full run takes **~20 min**. Adding a payment method on the Voyage dashboard lifts the throttle (still free under 200M tokens); the pacing then just becomes harmless overhead.
	- writes `profile/corpus_index.json` — a git-ignored, regenerable cache holding provenance metadata plus one record per chunk (`source`, `type`, `chunk_index`, `text`, `embedding`). 
- Run it occasionally (a one-off, like prebuild), and re-run it whenever the corpus changes.

## Eval - Execution results

#### Confusion matrix
| Metric | Approach 0 | Approach 1 | Approach 2 |
|--|--|--|--|
| TP (hit, flagged & relevant) | 14 | 4 | 14 |
| FP (false alarm, flagged but dud) | 120 | 8 | 70 |
| TN (correct skip) | 186 | 298 | 236 |
| FN (miss, skipped but relevant) | 21 | 31 | 21 |

#### Metrics

| Metric | Approach 0 | Approach 1 | Approach 2 |
|--|--|--|--|
| Precision (of flagged, % good) | 0.1045 | 0.3333 | 0.1667 |
| Recall    (of good, % caught) | 0.4000 | 0.1143 | 0.4000 |

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
