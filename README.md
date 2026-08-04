## Requirements

- Find the trending HackerNews articles users will be interested to read
- Update the filtered list of items every N hours

## Architecture

### Approach 1
---

#### Solution
Classify whether an article is a CS Tech Blog or not using `Generic "CS Technical" filter`

#### Sequence Diagram

![Approach 1 - Sequence Diagram](documentations/sequenceDiagrams/tech-new-approach-0.png)

### Approach 2
---

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
	- Cost Reduction
		- distilled profile JSON ensures reduced content being added to the prompt and not the entire bunch of readmes
		- this reduces the input tokens and hence cost
	- Stability
		- The interests profile is generated once currently
		- If this is generated during every run, then this would mean we would have a slightly different version of "user profile interests" as LLM output is non-deterministic and can vary slightly everytime
	- Inspectability
		- The flow is inspectable due to the artifact - `user profile interests json` file that is generated
		- Without the artifact, the only observable is the final ID list

### Approach 3 - Use RAG
---

#### Solution

- Make use of all the data available for the user
	- Readmes of repositories created by user
	- Blogs written by user in various platforms (medium, substack)
	- Reasearch papers read by user
- Then extract his interests to retrieve the news items the user will be interested int

##### Step 1
As you can see, the number of resources belonging to user can increase to a huge number.

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

### Approach 5 - Add the user's reading notes to the corpus
---

#### Solution

- Approach 3's corpus covers three kinds of user data, but misses a fourth
	- What the user **built** — repo READMEs
	- What the user **published** — blogs
	- What the user **studied** — white papers
	- What the user has been **reading recently** — missing
- Close that gap with the notes the user takes while reading
- Tried on **arm 2 only**, as an experiment

##### Step 1
- Add the reading notes to the corpus as `notes-*.md` files in `profile/corpus/`
	- One file per reading session: the blog's title, its URL, then the bullets taken while reading
- Re-run `embed` to rebuild `profile/corpus_index.json` over the widened corpus

##### Step 2
- Classify with arm 2 exactly as before — retrieval now selects from a corpus that
  includes the notes
- **No retrieval change is needed for any of this**
	- `readCorpusFiles` already reads every regular, non-hidden file in `profile/corpus/`, so notes are chunked and embedded with no wiring at all
	- `RETRIEVAL_TOP_K`, `cosineSimilarity`, `topKBySimilarity`, `RetrieveContext` and the arm 2 prompt are untouched
	- The only production edit is a `note` case in `rag.inferType`, and even that is cosmetic — `Chunk.Type` is stamped at index-build time, never read by retrieval or ranking, and exists so the built index can be inspected with `jq`

## Run modes (arms)

- The classifier flow is selected **explicitly** by an `arm` argument.
- Each arm picks exactly one `UserProfile` initialization and one classification flow.
- Each of the classification flow errors when its required data is absent rather than silently falling back.

| arm | UserProfile | Flow | Errors if… |
|-----|-------------|------|------------|
| `0` | none | generic/static "technical CS" prompt | never — production default |
| `1` | `summary` + `interests` from `profile/user_context.json` | interests injected into the prompt | the distilled JSON is missing (run `prebuild` first) |
| `2` | top-k corpus excerpts retrieved for the batch being classified | excerpts inlined into the prompt as evidence of the reader's interests | `profile/corpus_index.json` is missing, or retrieval fails (run `embed` first) |
| `3` | corpus excerpts retrieved **per story** and pooled, capped at `RETRIEVAL_POOL_CAP` | same prompt *template*, corpus and index as arm 2; excerpt *selection* differs — and so does excerpt **count** (up to 20 vs arm 2's 5), see the confound note below | same as arm 2 |

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

# Approach 4
go run . calibrate-floor > scores.csv   # measure rag.RETRIEVAL_SIMILARITY_FLOOR (free, no Claude call)
go run . 3              # normal run, arm 3 (RAG, per-story retrieval)
go run . eval 3         # eval under arm 3 — same free-tier throttling as arm 2

# Approach 5 — reading notes in the corpus (arm 2)
go run . embed          # picks up notes-*.md with no other wiring
go run . eval 2         # arm 2 is unchanged; only the corpus behind it widened
```

## Eval - Execution results

341 labelled stories, 35 relevant (~10% base rate). One run per arm — the model is
non-deterministic, so these are point estimates, not settled values.

Columns are labelled by **arm** (the CLI argument), since the "Approach N" numbering
above is offset by one and would collide here.

The last column is arm 2 run against the **post-notes** corpus (Approach 5); every
other column was measured against the pre-notes index.

#### Confusion matrix
| Metric | Arm 0 (static) | Arm 1 (interests) | Arm 2 (RAG, blended query) | Arm 3 (RAG, per-story) | Arm 2 (RAG, blended query + notes) | Arm 3 (RAG, per-story + notes)
|--|--|--|--|--|--|--|
| TP (hit, flagged & relevant) | 14 | 4 | 14 | 10 | 22 | 19 |
| FP (false alarm, flagged but dud) | 120 | 8 | 70 | 63 | 80 | 76 |
| TN (correct skip) | 186 | 298 | 236 | 243 | 226 | 230 |
| FN (miss, skipped but relevant) | 21 | 31 | 21 | 25 | 13 | 16 |

#### Metrics

| Metric | Arm 0 (static) | Arm 1 (interests) | Arm 2 (RAG, blended query) | Arm 3 (RAG, per-story) | Arm 2 (RAG, blended query + notes) | Arm 3 (RAG, per-story + notes) | 
|--|--|--|--|--|--|--|
| Precision (of flagged, % good) | 0.1045 | 0.3333 | 0.1667 | 0.1370 | 0.2157 | 0.2000 |
| Recall    (of good, % caught) | 0.4000 | 0.1143 | 0.4000 | 0.2857 | 0.6286 | 0.5429 |

#### Reading the results

**Reading notes are the largest gain recorded so far.** Against the post-notes
index arm 2 scored recall **0.6286** at precision **0.2157** — up from 0.4000 /
0.1667 — clearing issue #22's win condition (recall > 0.50 at precision ≥ 0.1667)
and its hold-ground criterion (22 TP against a floor of 14).

**The one criterion it missed is FP < 70: false alarms rose to 80.** That target
was set to make the recall gain unbuyable with false alarms, so the miss is real
and is recorded as a miss. It reads mildly, though: precision rose at the same
time, so the extra false alarms came attached to proportionally *more* true
positives rather than being spent freely. Flagged count went 84 → 102, a net +18
that decomposes as **+8 TP and +10 FP** — a 44% relevant rate on the net
addition, against the 17% of arm 2's pre-notes flagged set overall. That is a
*net* comparison, not a subset diff: the two flagged sets are not nested, so it
bounds the trade rather than naming which stories moved.

**The gain is large enough to clear the measured noise, unlike every other delta
in this table.** Two repeat `eval 2` runs against the *same* pre-notes index
scored recall 0.2571 and 0.3714 beside the tabled 0.4000 — mean **0.343 ±
0.076**, precision mean **0.148 ± 0.022**. The tabled baseline is thus the best
of three samples, and the post-notes run still sits ~3.8 standard errors above
the baseline *mean*. The spread matches the ~8pp binomial standard error at 35
positives, and since retrieval is deterministic given a fixed index, it is
entirely Claude's sampling: `claudeapi.Request` sends no `temperature` field, so
calls run at the API default of 1.0. Pinning it to 0 is the one-line lever if
noise ever blocks a decision — not taken, because it would re-base every number
in this section at once.

**Notes displace heavily, but why they win is not established.** The two note
chunks are 1.2% of the index and take the top-1 match for 158 of 341 labelled
titles, pushing the previous leader — a repo README — from 120 top-1 wins to 49.
That much is measured. Whether they win on chunk length or on topical overlap
with an AI-heavy title stream is not separated, and separating it would mean
re-chunking, which invalidates every other column in the table. Issue #22's
pre-run prediction that it was length does **not** survive checking and is
retracted in that thread.

**Arm 3 did not beat arm 2.** It missed both acceptance targets set in issue #19
(FP ≤ 35, recall ≥ 0.40): FP came in at 63 and recall at 0.2857.

It flagged 73 stories against arm 2's 84. Of the 11 it stopped flagging, **4 were
relevant and 7 were not** — a 36% hit rate among the dropped stories, against the
17% hit rate of arm 2's flagged set overall. Per-story retrieval pruned the
*right-leaning* part of the flagged set, which is the opposite of what a better
context should do.

**That said, the gap is inside the noise.** With 35 positives, the standard error
on recall is about 8 percentage points; the arm 2 → arm 3 difference is 11 points,
roughly 1.4 standard errors. The defensible claim is **"arm 3 is not better"**, not
"arm 3 is worse". Separating those would need several runs per arm.

**And the comparison is confounded: two variables changed, not one.** Arm 3 was
intended as a single-variable change (excerpt *selection*), and the prompt
template is genuinely shared — a test asserts the two arms render byte-identical
prompts from the same excerpts, so prompt *wording* is ruled out. Prompt *size* is
not: arm 2 injects exactly `RETRIEVAL_TOP_K` = 5 excerpts, arm 3 **up to**
`RETRIEVAL_POOL_CAP` = 20. Every excerpt is ~800 words inlined verbatim, so arm
3's prompt was larger — by up to 4×, and dilution and position effects in long
stuffed contexts are a well-documented cause of exactly the kind of recall drop
observed. *How much* larger is not actually recorded: the per-call token figure
below is arithmetic **assuming** 20 chunks, not a measurement, so it cannot be
cited as evidence the cap bound, and `calibrate-floor`'s pool-cap section is
dedupe-blind. Only a `count_tokens` call or a dedupe-aware re-run settles it.
**And it genuinely matters which:** "up to 20" includes 5, so if the deduped pool
ran near 5 per batch there was no volume gap and the delta *is* attributable to
selection after all. The confound is unresolved, not established — that is the
whole reason the recorded delta can't be read either way. **So the recorded delta cannot distinguish
"per-story retrieval didn't help" from "the bigger prompt hurt".** The conclusion
that survives regardless is the one from the retrieval statistics below (AUC
0.659): the ranking both arms select from barely separates the classes.
`rag.TestRetrievalArmsInjectDifferentExcerptVolumes` pins this gap so it stays
visible in code. **De-confounding is cheap** — set `RETRIEVAL_POOL_CAP` =
`RETRIEVAL_TOP_K` = 5 and re-run `eval 2` and `eval 3` together (~$0.08 each at 5
chunks, since arm 3's cost is dominated by the excerpt count). Cap-matched, the
comparison is genuinely single-variable.

**Why it did not work — measured before the eval, not after.** `calibrate-floor`
scored every labelled title against the corpus and found **AUC 0.659, d′ 0.635**:
relevant and irrelevant stories' best-chunk scores overlap heavily. Arms 2 and 3
both select from that same weak ranking and differ only in *how* they select. No
selection strategy can rescue a ranking that barely separates the two classes, so
the ceiling was already visible in the retrieval statistics.

**`RETRIEVAL_SIMILARITY_FLOOR` shipped at 0.0 and is inert, not tested.** The best
floor available (0.2336) sits *below* the ~0.32 that `RETRIEVAL_POOL_CAP = 20`
already enforces by truncation, so no floor both fires and helps. Arm 3's numbers
therefore measure per-story retrieval plus pooling, with floor-filtering never in
effect.

**Standing conclusions.** Arm 2 with notes is the best configuration measured:
best recall of any arm by a wide margin (0.6286) at the second-best precision
(0.2157), and the only recorded delta that clears the run-to-run noise. Corpus
*content* has so far bought more than any retrieval-strategy change did. Without
notes, arm 2 still dominates arm 0 — identical recall (0.4000) at **60% fewer
false positives** (70 vs 120) — so blended-query retrieval does pay for itself
over the static ruleset, though that "identical recall" compares two n=1 draws
and arm 2's own mean is 0.343, not 0.4000. Arm 1 remains the precision leader
(0.3333 against a 10% base rate) at the worst recall by far, so the choice
between arms 1 and 2 is a precision/recall preference, not a quality ranking.
Arm 3 adds nothing over arm 2 **as configured**, and its estimated eval cost is ~3.5× arm 2's
(~$0.29 vs ~$0.08). That estimate assumes a full 20-chunk pool per call; it is the
same assumption the confound above turns on, so it is a projection of arm 3's
ceiling, not a measured bill.

Next levers, in cost order: repeat `eval 2` on the post-notes index a couple more
times (~$0.08 each) — the notes result is n=1 against a baseline now known to
swing ±7.6pp, and it is the number the next decision will lean on; then more
reading notes, the cheapest lever with a measured payoff; then the cap-matched
arm 2 vs arm 3 re-run above (~$0.16 total, and the only way to attribute that
delta — note it must be re-run against the post-notes index, since arm 3's tabled
numbers are now stale); then smaller chunks (currently 800 words — a ~9-word
title averaged against an 800-word window dilutes the signal, and re-testing at
the retrieval layer via `calibrate-floor` is free); then reranking — evaluated
first as a scoring function (AUC on a subsample) before any arm is wired.

## Setup

### Secrets & environment

The project needs two secret API keys and one non-secret config value:

| Name | Type | Used by | Where it lives |
|------|------|---------|----------------|
| `ANTHROPIC_API_KEY` | secret | All Approaches | macOS Keychain |
| `VOYAGE_API_KEY` | secret | Approach 3 | macOS Keychain |
| `GITHUB_USERNAME` | non-secret | Approach 2 | `.env` (see `.env.example`) |

### How to initialize the API keys (macOS Keychain)?

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

## Run script

Use any one of the modes mentioned under the [Run modes](#run-modes-arms) section.

## License

Released under the [MIT License](LICENSE).
