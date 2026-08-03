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

The corpus so far covers what the user **built** (repo READMEs), **published** (blogs)
and **studied** (white papers) — but not what they have been **reading recently**.
Reading notes close that gap: drop `notes-*.md` files (blog title, URL, then the
bullets taken while reading) into `profile/corpus/` and re-run `embed`.

Tried on **arm 2 only**, as an experiment.

**No retrieval change is needed.** `readCorpusFiles` already reads every regular,
non-hidden file in `profile/corpus/`, so notes are chunked and embedded with no
wiring; retrieval, ranking, `RETRIEVAL_TOP_K` and the arm 2 prompt are untouched.
The only code change is a `note` case in `rag.inferType` — and even that is
cosmetic, since `Chunk.Type` is written into the index for inspection (`jq`) and
never read by retrieval.

#### Notes

- **Not purely "more corpus".** A note is ~50 words against an 800-word chunk
  window, so each note becomes one short chunk, and short chunks tend to score
  higher against short titles. Notes are expected to take a disproportionate share
  of the top-5 — that is the intended displacement mechanism, but it means the
  change is "more corpus **and** shorter chunks".
- **Predicted ranking outcome, measured free before spending:** a bare regex for
  `AI|LLM|GPT|agent|…` scores AUC **0.6713** on the labelled set, against the full
  RAG pipeline's **0.6580**. AI-ish titles are 90 of 341 and hold 20 of the 35
  positives (22.2% relevant vs 6.0% elsewhere), so topic is a real signal — but
  *within* the AI group the corpus separates relevant from irrelevant by
  **+0.0005**, i.e. not at all. So a post-notes `calibrate-floor` AUC near 0.66 is
  the **predicted** result, not a failure; the bet is on excerpt usefulness to
  Claude, which only the eval can see.
- **Precision risk:** 70 of those 90 AI-ish titles are irrelevant, so better
  AI-*topic* retrieval means flagging more from a group that is 78% wrong. Notes
  could plausibly *lower* precision — the retrieval-key confirmation-bias failure
  mode, now with concrete evidence behind it.

#### Acceptance criteria

| Side | Criterion | Why |
|--|--|--|
| Hold ground | recall ≥ 0.40 **and** TP ≥ 14 | arm 2's current position |
| Don't buy it with false alarms | precision ≥ 0.1667 **and** FP < 70 | arm 2's current numbers |
| Win condition | recall > 0.50 at precision ≥ 0.1667 | a single run wobbles ~±0.04 recall at 35 positives |

⚠️ **These thresholds are anchored to arm 2's tabled run, which repeat runs have
since shown to be the *highest* of three samples** (recall 0.4000 against a mean
of 0.343 ± 0.076 — see *Run-to-run variance* above). Kept unchanged deliberately,
but read the result accordingly: a post-notes run landing near **0.34 recall / 12
TP** is sitting on the baseline mean and is a *null* result, not a regression,
even though it fails the "hold ground" row. Only a post-notes run below ~0.27
(the baseline's own worst sample) is evidence of actual harm, and only the win
condition — a >0.10 recall shift — clears the noise in the other direction. The
n=1 wobble noted in the last row is the *within-run* estimate; the measured
between-run spread is roughly double it.

#### Run order

`profile/corpus_index.json` is a single unversioned file, so `embed` destroys the
pre-notes state. Snapshot and measure the baseline first:

```bash
cp profile/corpus_index.json profile/corpus_index.pre-notes.json
go run . calibrate-floor > profile/scores-pre-notes.csv   # baseline ranking + top-source table ($0.00)
go run . eval 2                                           # fresh baseline, same session (~$0.08)
# ... add notes-*.md to profile/corpus/ ...
go run . embed                                            # ~20 min, $0.00
go run . calibrate-floor > profile/scores-notes.csv       # free ranking + displacement check
go run . eval 2                                           # the after-run (~$0.08)
```

Restore with `cp profile/corpus_index.pre-notes.json profile/corpus_index.json`.
Arm 3 reads the same index, so **its recorded numbers go stale once `embed`
re-runs**. `profile/corpus_index*.json` and `profile/scores*.csv` are git-ignored,
so neither the snapshot nor the CSVs get committed.

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

# Approach 5 — reading notes in the corpus (arm 2); full run order above
go run . embed          # picks up notes-*.md with no other wiring
```

`calibrate-floor` writes the per-title CSV (`best_score,label,best_source,title`)
to **stdout** and its report — distribution, survival, **top-1 match by corpus
file**, and the verdict — to **stderr**, so `> scores.csv` keeps the two apart.
The `best_source` column and the top-source table are what make "did a corpus
change displace the documents that were winning?" a repeatable check: run the
command before and after the change and diff the tables.

## Eval - Execution results

341 labelled stories, 35 relevant (~10% base rate). One run per arm — the model is
non-deterministic, so these are point estimates, not settled values.

Columns are labelled by **arm** (the CLI argument), since the "Approach N" numbering
above is offset by one and would collide here.

#### Confusion matrix
| Metric | Arm 0 (static) | Arm 1 (interests) | Arm 2 (RAG, blended query) | Arm 3 (RAG, per-story) | Arm 3 (RAG, per-story + notes)
|--|--|--|--|--|--|
| TP (hit, flagged & relevant) | 14 | 4 | 14 | 10 | 22 |
| FP (false alarm, flagged but dud) | 120 | 8 | 70 | 63 | 80 |
| TN (correct skip) | 186 | 298 | 236 | 243 | 226 |
| FN (miss, skipped but relevant) | 21 | 31 | 21 | 25 | 13 |

#### Metrics

| Metric | Arm 0 (static) | Arm 1 (interests) | Arm 2 (RAG, blended query) | Arm 3 (RAG, per-story) | Arm 3 (RAG, per-story + notes)
|--|--|--|--|--|--|
| Precision (of flagged, % good) | 0.1045 | 0.3333 | 0.1667 | 0.1370 | 0.2157 |
| Recall    (of good, % caught) | 0.4000 | 0.1143 | 0.4000 | 0.2857 | 0.6286 |

#### Run-to-run variance — measured for arm 2 (n = 3)

⚠️ **The arm 2 column above is one sample, and it is the highest of three.** Two
further `eval 2` runs against the *same* pre-notes index (2026-08-03) came back:

| Run | TP | FP | TN | FN | Precision | Recall |
|--|--|--|--|--|--|--|
| tabled above | 14 | 70 | 236 | 21 | 0.1667 | 0.4000 |
| repeat 1 | 9 | 64 | 242 | 26 | 0.1233 | 0.2571 |
| repeat 2 | 13 | 72 | 234 | 22 | 0.1529 | 0.3714 |
| **mean ± sd** | **12 ± 2.6** | **68.7 ± 4.2** | | | **0.148 ± 0.022** | **0.343 ± 0.076** |

The ±7.6pp spread on recall matches the ~8pp binomial standard error at 35
positives, so this is ordinary sampling noise, not a defect — and it is the
concrete version of the "one run is a point estimate" caveat this section opens
with. Retrieval is deterministic given a fixed index (same titles, same batches,
same embeddings), so the variance is entirely **Claude's sampling**:
`claudeapi.Request` sends no `temperature` field, so every call runs at the API
default of 1.0. Pinning it to 0 is the obvious lever if the noise ever blocks a
decision; it is deliberately **not** done here, because it would re-base every
number in this section at once.

**How to read every single-run number in this table, therefore:** as a draw from
a distribution roughly this wide, not as the arm's value. Two consequences worth
stating: the arm 2 → arm 3 gap discussed below is even less separable than the
1.4-standard-error estimate given there, and the "identical recall" claim under
*Standing conclusions* compares two n=1 draws — arm 2's mean is 0.343, not
0.4000, and arm 0 has never been repeated at all.

#### Reading the results

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

**Standing conclusions.** Arm 2 dominates arm 0 on false positives — **60% fewer**
(70 vs 120) — so blended-query retrieval does pay for itself over the static
ruleset. The recall half of that claim no longer stands as written: it read
"identical recall (0.4000)", which compared two single draws, and arm 2's
three-run mean is **0.343** while arm 0 has never been repeated (see *Run-to-run
variance* above). Treat it as "arm 2 buys a large FP reduction at a recall cost
that is not resolved". Arm 1 remains the precision leader (0.3333
against a 10% base rate) at the worst recall by far, so the choice between arms 1
and 2 is still a precision/recall preference, not a quality ranking. Arm 3 adds
nothing over arm 2 **as configured**, and its estimated eval cost is ~3.5× arm 2's
(~$0.29 vs ~$0.08). That estimate assumes a full 20-chunk pool per call; it is the
same assumption the confound above turns on, so it is a projection of arm 3's
ceiling, not a measured bill.

Next levers, in cost order: the cap-matched arm 2 vs arm 3 re-run above (~$0.16
total, and the only way to attribute the recorded delta), smaller chunks
(currently 800 words — a ~9-word title averaged against an 800-word window
dilutes the signal, and re-testing at the
retrieval layer via `calibrate-floor` is free), wider corpus coverage, then
reranking — evaluated first as a scoring function (AUC on a subsample) before any
arm is wired.

**Pending: Approach 5 (reading notes in the corpus).** The code is in; the numbers
are not — the notes and the re-`embed` come next, and the before/after
`calibrate-floor` + `eval 2` results get recorded here either way. Every number in
this section was measured against the **pre-notes** index; once `embed` re-runs
over the notes, arm 3's row in particular goes stale, since it reads the same
index and will not be re-run.

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
