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

### Approach 6 - Rerank the retrieved chunks with a cross-encoder
---

#### The problem with cosine alone

- Arms 2 and 3 rank chunks by cosine similarity between two vectors that **never
  saw each other**
	- A chunk's vector is computed at `embed` time, before any story exists, and
	  compresses ~800 words into a single point — a topical *average*
	- So a chunk whose relevance lives in one paragraph out of eight is averaged
	  down by the other seven
- `calibrate-floor` measured how weak that ranking is directly: AUC 0.659, d' 0.635
  pre-notes — relevant and irrelevant stories' best-chunk scores overlap heavily

#### Solution

- Add a **second ranking stage** on top of arm 3's retrieval — arm `4`
- A cross-encoder reads the story title and the chunk **together in one forward
  pass**, so every token of the title can attend to every token of the chunk.
  Nothing is averaged, and the score is produced for that specific pair
- It cannot replace cosine, only follow it: a cross-encoder emits no reusable
  vector, so it cannot be indexed and must run live for every (title, chunk) pair.
  Scoring the whole index per story would cost 168 pairs instead of 6
	- **Cosine narrows, the cross-encoder reorders**

##### The flow, per story

1. Cosine-select `RERANK_CANDIDATES_PER_STORY` (6) candidates — at a *permissive*
   floor, since this stage is now about recall, not relevance
2. Send (title, 6 chunks) to Voyage `/v1/rerank` with `rerank-2.5` — with no
   `top_k`, so every candidate comes back scored
3. Keep the top `RERANK_TOP_K_PER_STORY` (2) by relevance score, and log all 6
   scores — the 4 discarded ones are already paid for and are what a future
   rerank floor gets calibrated against
4. Pool, dedupe and cap exactly as arm 3 does — `poolChunks` is unchanged, so the
   pool is now ordered by cross-encoder score instead of cosine

##### What is deliberately identical to arm 3

Same corpus, same index, same queries (the batch's titles, one per story), same
`poolChunks`, same `RETRIEVAL_POOL_CAP`, same prompt, and the **same number of
chunks kept per story** — so the ranking is the single variable, and the
excerpt-volume confound that spoiled the arm 2 vs arm 3 comparison does not apply
between arms 3 and 4.

##### Cost and runtime

| | `eval 4` (341 stories, 12 Claude calls) | production run (30 stories) |
|--|--|--|
| Voyage rerank calls | 341 | 30 |
| Wall clock | **~5 hours** | ~26 min |
| Voyage cost | $0.00 (free tier) | $0.00 |
| Claude cost | ~$0.29 | ~$0.001 |

The wall clock is entirely pacing: one cross-encoder call scores one query, so N
stories cost N requests, and each is ~7.9K tokens against the free tier's 10K/min
— about 52s apart. Adding a payment method on the Voyage dashboard lifts the
throttle and the pacing becomes harmless overhead.

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
| `4` | the same per-story excerpts, **reranked by a cross-encoder** before pooling | same prompt template, corpus, index and queries as arm 3; only the *ranking* differs — cosine narrows to 6 candidates per story, `rerank-2.5` reorders them, top 2 are kept. Volume-matched to arm 3 by construction | same as arm 2, plus a `/v1/rerank` failure |

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

# Approach 6 — reranking on top of RAG
go run . 4              # normal run, arm 4 (RAG per-story + cross-encoder rerank)
go run . eval 4         # eval under arm 4 — ~5 HOURS on Voyage's free tier, see below

# Approach 5 — reading notes in the corpus (arm 2)
go run . embed          # picks up notes-*.md with no other wiring
go run . eval 2         # arm 2 is unchanged; only the corpus behind it widened

# Reading the eval history back (free — no API call, writes nothing)
go run . eval-report    # every run, per-configuration mean ± spread, then per-story stability
```

Every `eval` also writes a record of what it used to `evalRuns/` — see
*Eval run records* below.

## Eval run records (`evalRuns/`)

Every `go run . eval <arm>` leaves two files behind, so a number in the table
below can be traced back to what produced it. Both are named
`<UTC timestamp>-arm-<N>` — e.g. `20260806T091500Z-arm-2` — which is also the
record's `run_id`.

| File | What it answers |
|------|-----------------|
| `evalRuns/<run_id>.json` | *What did this run use?* — arm, git SHA, model, the hashes of its data, and the confusion matrix |
| `evalRuns/<run_id>.csv` | *What did it decide about each story?* — one row per dataset item: `run_id, story_id, title, label, predicted` |

The JSON gives the counts; the CSV gives the per-story verdicts behind them.
Comparing two runs is a `join` on `story_id` between their CSVs.

```json
{
  "run_id": "20260806T091500Z-arm-2",
  "arm": 2,
  "git_sha": "fc057f7…",
  "dataset_hash": "9f2c…",
  "corpus_index_hash": "4a71…",
  "model": "claude-haiku-4-5-20251001",
  "metrics": { "tp": 22, "fp": 80, "tn": 226, "fn": 13,
               "precision": 0.2157, "recall": 0.6286 }
}
```

**Why the hashes.** The two inputs that decide the numbers — the labelled dataset
and `profile/corpus_index.json` — are both git-ignored and overwritten in place.
Approach 5 re-based every arm 2 and arm 3 number by re-embedding a widened
corpus, and the pre-notes index survived only because it was `cp`'d by hand
first. So each run archives its inputs content-addressed:

```
evalRuns/datasets/<sha256>.json       # the labelled dataset it scored against
evalRuns/corpus_index/<sha256>.json   # the index it retrieved from (arms 2 & 3)
```

Write-if-absent, so repeat runs over unchanged inputs add nothing; a *changed*
input is stored beside the old one rather than replacing a snapshot an earlier
record still points at. The hash is plain sha256 of the file's raw bytes, so
`shasum -a 256 profile/corpus_index.json` reproduces it. `go run . embed`
archives the index too, which captures it at build time rather than at first
eval.

`corpus_index_hash` is **absent** for arms 0 and 1 — they never load the index.

Two limitations worth knowing before trusting a record:

- **`git_sha` is the last commit, not the working tree.** An uncommitted edit to
  a tuning constant is invisible, so commit tuning changes before measuring them.
- **Re-embedding an unchanged corpus produces a new hash**, because the index
  stamps a fresh `generated_at` and the hash covers the whole file. Harmless —
  it stores one extra copy — but it means "different hash" does not always mean
  "different corpus".

The whole directory is git-ignored: it is local run history, regenerable only by
paying for another run.

### Reading the records back — `go run . eval-report`

The records answer "what did this run use". `eval-report` answers the question
they were written for: **how much does a configuration's score move between
runs?** A single eval is a point estimate — the classifier is non-deterministic
— so a gap between two arms is only a result once it clears the run-to-run
spread. Until now that spread was computed by hand.

The command reads `evalRuns/` and prints three sections. It costs **$0.00**,
makes no API call, and **writes nothing**.

```
=== Eval runs (4) ===

run_id                  arm  git_sha  dataset_hash  corpus_index  model                      TP  FP   TN   FN  precision  recall
20260806T084423Z-arm-2  2    1b4a0be  a59941fa34f7  3daf2eee4e65  claude-haiku-4-5-20251001  24  79   227  11  0.2330     0.6857
20260806T085514Z-arm-2  2    1b4a0be  a59941fa34f7  3daf2eee4e65  claude-haiku-4-5-20251001  21  82   224  14  0.2039     0.6000
20260806T090351Z-arm-0  0    1b4a0be  a59941fa34f7  —             claude-haiku-4-5-20251001  17  117  189  18  0.1269     0.4857
20260806T090525Z-arm-3  3    1b4a0be  a59941fa34f7  3daf2eee4e65  claude-haiku-4-5-20251001  20  70   236  15  0.2222     0.5714

=== Grouped by (arm, git_sha, model, dataset_hash, corpus_index_hash) — 3 groups ===

arm  git_sha  dataset_hash  corpus_index  model                      n  TP    FP     TN     FN    precision        recall
2    1b4a0be  a59941fa34f7  3daf2eee4e65  claude-haiku-4-5-20251001  2  22.5  80.5   225.5  12.5  0.2184 ± 0.0206  0.6429 ± 0.0606
0    1b4a0be  a59941fa34f7  —             claude-haiku-4-5-20251001  1  17.0  117.0  189.0  18.0  0.1269           0.4857
3    1b4a0be  a59941fa34f7  3daf2eee4e65  claude-haiku-4-5-20251001  1  20.0  70.0   236.0  15.0  0.2222           0.5714

=== Per-story stability — arm 2, 1b4a0be, dataset a59941fa34f7,
     index 3daf2eee4e65, claude-haiku-4-5-20251001 (n=2 runs) ===

                  never     sometimes  always
                  (0 of 2)  (1 of 2)   (2 of 2)
relevant (35)     11        3          21
irrelevant (296)  211       11         74
```

- **Table 1** — one row per run, sorted by `run_id` (chronological by
  construction). `git_sha` is abbreviated to 7 so it pastes into `git show`; the
  content hashes to 12, enough to name one archived file. The model is printed in
  full — it is a grouping key, and truncating it could make two different models
  look like one.
- **Table 2** — one row per *configuration*: runs are repeats of the same
  experiment only if they shared all five of arm, commit, model, dataset and
  corpus index. Counts are means; precision and recall carry the **sample**
  standard deviation (n−1 denominator — the runs are draws from a
  non-deterministic process, not a complete population).
- **`±` appears only when it was measured.** At n=1 there is no spread, so no `±`
  is printed. `± 0.0000` would read as perfect reproducibility, which is the
  opposite of what one run tells you — and n=1 is the common case.
- **Precision and recall are averaged per run**, never recomputed from the summed
  counts: pooling would cancel the per-run variation inside the fraction and
  average away exactly what the table measures.
- An unreadable record file is **skipped with a warning on stderr** and the skip
  count appears in the header (`=== Eval runs (3, 1 skipped) ===`), so a short `n`
  is visible in the report rather than only in a log line.
- **Table 3 — per-story stability**, one block per configuration with 2 or more
  runs. Tables 1 and 2 report counts, and counts are anonymous: `24 TP` is 24
  tally marks, not 24 named stories, so a mean of 22.5 across two runs cannot say
  whether it was the *same* stories both times. Reading each run's predictions CSV
  answers it: a story flagged by every run, by some, or by none. Above, arm 2
  catches 21 of the 35 relevant stories in both runs, flips on 3, and misses 11 in
  both — which splits the lump mean FN into work that needs retrieval, prompt or
  corpus changes (the 11, which re-running will not fix) and work a sampling
  change might convert (the 3). The irrelevant row answers the same question for
  false alarms, free, since it is the same computation.
- The stability rows count **331 distinct `story_id`s, not the 341 dataset rows** —
  10 ids appear twice, all irrelevant, which is why that row reads 296. All 35
  relevant stories are distinct, so nothing on the relevant side is affected.
- Groups with **one run print no block**, for the same reason they print no `±`.
  And at n=2 the block is thin by nature: "sometimes" can only mean 1 of 2, and
  "never" also absorbs a story that was merely unlucky twice.

Two caveats it inherits from the records and cannot detect: `git_sha` is HEAD
rather than the working tree, so two runs sharing a sha may have run different
uncommitted code and would group as one configuration; and arm 1's
`profile/user_context.json` is not hashed, so arm-1 rows group on a key missing an
input that decides their numbers. A suspiciously wide `±` should suspect the first
of these before it suspects the model.

## Eval - Execution results

341 labelled stories, 35 relevant (~10% base rate). One run per arm — the model is
non-deterministic, so these are point estimates, not settled values.

Rows are labelled by **arm** (the CLI argument), since the "Approach N" numbering
above is offset by one and would collide here. **One row per run** — add a new row
for each result rather than widening the table.

The last two rows were run against the **post-notes** corpus (Approach 5); every
other row was measured against the pre-notes index.

#### Confusion matrix

| Run | TP (hit, flagged & relevant) | FP (false alarm, flagged but dud) | TN (correct skip) | FN (miss, skipped but relevant) |
|--|--|--|--|--|
| Arm 0 (static) | 14 | 120 | 186 | 21 |
| Arm 1 (interests) | 4 | 8 | 298 | 31 |
| Arm 2 (RAG, blended query) | 14 | 70 | 236 | 21 |
| Arm 3 (RAG, per-story) | 10 | 63 | 243 | 25 |
| Arm 2 (RAG, blended query + notes) | 22 | 80 | 226 | 13 |
| Arm 3 (RAG, per-story + notes) | 19 | 76 | 230 | 16 |
| Arm 3 (RAG, per-story + notes + similarity_floor) | 19 | 83 | 223 | 16 |
| Arm 4 (RAG, per-story + notes + rerank) | — | — | — | — |

#### Metrics

| Run | Precision (of flagged, % good) | Recall (of good, % caught) |
|--|--|--|
| Arm 0 (static) | 0.1045 | 0.4000 |
| Arm 1 (interests) | 0.3333 | 0.1143 |
| Arm 2 (RAG, blended query) | 0.1667 | 0.4000 |
| Arm 3 (RAG, per-story) | 0.1370 | 0.2857 |
| Arm 2 (RAG, blended query + notes) | 0.2157 | 0.6286 |
| Arm 3 (RAG, per-story + notes) | 0.2000 | 0.5429 |
| Arm 3 (RAG, per-story + notes + similarity_floor) | 0.1863 | 0.5429 |
| Arm 4 (RAG, per-story + notes + rerank) | — | — |

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
re-chunking, which invalidates every other row in the table. Issue #22's
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
observed. *How much* larger is now recorded, though only for the post-notes run:
counting excerpts in the run log gives a mean of **18.9 per call**, with the cap
binding at a full 20 in 10 of the 12 batches (see the floor section below). So the
volume gap was real — "up to 20" was not secretly running near 5, and arm 3 really
did inject ~4× arm 2's excerpts. That settles whether a gap existed, but not the
attribution: these numbers still cannot say whether selection or size drove the
delta. What speaks to *that* is the floored arm 3 run below, which cuts the pool
to 7.1 and moves recall not at all. **So the recorded delta cannot on its own distinguish
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

**`RETRIEVAL_SIMILARITY_FLOOR` was inert before the notes, and is not any more.**
Against the pre-notes corpus the best available floor (0.2336) sat *below* the
~0.32 that `RETRIEVAL_POOL_CAP = 20` already enforces by truncation, so no value
both fired and helped — every arm 3 number above the last one was measured with
floor-filtering never in effect. Re-calibrating against the post-notes corpus
(issue #25) moved the best floor to **0.3330**, above the cap's threshold for the
first time. It was set, and `eval 3` re-run.

**The floor removes 63% of the retrieved context and changes nothing that
matters.** Counting `--- Excerpt N ---` in the two run logs
(`eval-run-logs/eval-3-run-post-notes` and `…-and-floor-set`) gives what each of
the 12 Claude calls actually received:

| Run | excerpts per batch (12 calls) | total | mean |
|--|--|--|--|
| Arm 3, no floor | 20 20 20 20 20 20 20 18 20 20 20 9 | 227 | 18.9 |
| Arm 3, floor 0.3330 | 10 7 9 7 9 4 4 7 6 6 13 3 | 85 | 7.1 |

**142 of 227 excerpts were dropped and not one relevant story was lost.** TP (19)
and FN (16) are identical across the two runs — not close, identical. The entire
delta is 7 stories moving from correct-skip to false-alarm, i.e. precision 0.2000
→ 0.1863, a 0.62 standard-deviation move against the ±0.022 precision spread
measured above. Re-running the same configuration moves precision more than the
floor did.

Cost follows the excerpt count: `eval 3` should now run around **$0.11–0.13**
rather than ~$0.29. That part is mechanical. The accuracy-neutrality is not — the
floor was fit on the same labelled set that then scored it, which is the most
favourable evaluation it will ever get, so treat "costs nothing" as an optimistic
reading until it is checked on data it was not tuned against.

**This also weakens the excerpt-volume explanation for arm 3's deficit.** Floored,
arm 3 runs at 7.1 excerpts per call — close to arm 2's fixed 5 — and its recall
did not move at all (0.5429 both), still below arm 2's 0.6286. If prompt *size*
had been what held arm 3 back, cutting it by 63% should have shown something. This
is a directional null rather than proof (single runs, and the arm 2 → arm 3 recall
gap is itself ~1 standard error), but it is the closest to a volume-matched
comparison recorded so far, and it points at per-story selection simply not
beating the blended query.

**A forecast that failed, kept visible because the failure is instructive.** The
floor was predicted to trim "a few chunks off the bottom of the pool": the
calibration reports the cap's implied floor as a median of **0.3216**, which makes
0.3330 look like it clears by 0.011. It trimmed 63%. That printed figure is
explicitly an *upper bound* — it is computed before deduplication, and the deduped
pool's 20th-ranked chunk sits well below it. Reading an upper bound as if it were
the operating point produced the wrong forecast. The tool said so in its own
output; the number was quoted and the caveat next to it was not.

**Residual risk: an absolute floor can empty the pool.** The smallest observed was
3 excerpts, so it did not happen here, but it can: `RETRIEVAL_POOL_CAP` is a
*relative* threshold and always returns its 20 best, whereas a floor on a batch
with no corpus support returns nothing and yields a prompt with no excerpts at
all. Add a fallback — top-N ignoring the floor when the pool comes back empty —
before anything depends on arm 3.

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
Arm 3 adds nothing over arm 2 **as configured**, at ~3.5× arm 2's eval cost
(~$0.29 vs ~$0.08). The 20-chunk-per-call assumption behind that estimate is now
corroborated by the run log (mean 18.9), so it is close to the real bill rather
than a ceiling projection. With `RETRIEVAL_SIMILARITY_FLOOR` = 0.3330 the pool
falls to 7.1 and the cost with it, to roughly $0.11–0.13 — arm 3 gets
substantially cheaper without getting any better.

Next levers, in cost order: repeat `eval 2` on the post-notes index a couple more
times (~$0.08 each) — the notes result is n=1 against a baseline now known to
swing ±7.6pp, and it is the number the next decision will lean on; then more
reading notes, the cheapest lever with a measured payoff; then the cap-matched
arm 2 vs arm 3 re-run above (~$0.16 total, and the only way to attribute that
delta — note it must be re-run against the post-notes index, since arm 3's tabled
numbers are now stale); then smaller chunks (currently 800 words — a ~9-word
title averaged against an 800-word window dilutes the signal, and re-testing at
the retrieval layer via `calibrate-floor` is free); then reranking — **now built
as arm 4, see below**.

#### Arm 4 (reranking) is built but NOT measured

The arm-4 row above is blank on purpose. Nothing about reranking has been scored;
`go run . eval 4` has not been run. Do not read the row as a null result.

**Its baseline is the `Arm 3 (RAG, per-story + notes)` row — 19 / 76 / 230 / 16,
precision 0.2000, recall 0.5429 — not the floored row below it.** That is a
deliberate choice about excerpt volume, the variable Approach 4 got caught by.
Arm 4 uses a permissive candidate floor and keeps 2 chunks from each of ~30
stories, so its pool should land near the no-floor arm 3 run's measured **18.9
excerpts per call**; arm 3 *at HEAD*, with `RETRIEVAL_SIMILARITY_FLOOR` = 0.3330,
runs at **7.1**. Comparing against the floored row would compare two things at
once again.

The cost of that choice: the comparison is **cross-commit and n=1 on both sides**.
Re-running arm 3 volume-matched as a fresh baseline is the fix, and is worth
paying for only if arm 4 lands near the acceptance line. If it is taken, the floor
change must be **committed first** — `git_sha` on the run record is HEAD, not the
working tree.

**Check the logged pooled count before reading any delta.** Arm 4 logs
`rag: arm 4 pooled N excerpts` per call. If N is not near 18.9, the comparison is
not volume-matched and the delta is confounded — this document records twice that
assuming a pool size instead of counting it produced a wrong conclusion.

Issue #27's acceptance criteria (FP ≤ 76, precision ≥ 0.2000, recall ≥ 0.5429)
*are* that baseline row exactly, so they are cleared by an exact tie. A result on
the line means **"did not lose ground"**, not "beat arm 3". At 35 positives the
standard error on recall is ~8pp, so anything under ~1 SE is noise.

The run also dumps every kept (story, chunk) pair's cosine **and** rerank score to
the log (`rag: rerank-score,<query>,<source>,<chunk_index>,<cosine>,<rerank>,<kept>`)
— **all 6 per story, not just the 2 kept**, since no `top_k` is sent and the
discarded scores cost nothing extra. Those lines are the input to a future
rerank-floor experiment, and a floor is a decision about where to cut, so it
needs both sides of the current cut. Grab them from the run log before it is
lost; the alternative is paying for a second ~5h run.

## Setup

### Secrets & environment

The project needs two secret API keys and one non-secret config value:

| Name | Type | Used by | Where it lives |
|------|------|---------|----------------|
| `ANTHROPIC_API_KEY` | secret | All Approaches | macOS Keychain |
| `VOYAGE_API_KEY` | secret | Approach 3 (`/v1/embeddings`) and Approach 6 (`/v1/rerank`) | macOS Keychain |
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
