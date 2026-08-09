package evalHarness

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shrirambalakrishnan/tech-news/claudeapi"
	"github.com/shrirambalakrishnan/tech-news/hackernews_classifier"
	"github.com/shrirambalakrishnan/tech-news/rag"
	"github.com/shrirambalakrishnan/tech-news/voyageapi"
)

// This file records WHAT an eval run used, so a number in the README can be
// traced back to the code, model and data that produced it.
//
// The eval harness answers "how good is the classifier". It does not answer
// "good under what" - which arm, which model, which commit's tuning constants.
// Today that context lives in whoever ran it, and it is gone by the next run.
// Every run now leaves a JSON record behind (issue #26).
//
// The record is DESCRIPTIVE, never an input: nothing reads it back to change
// how a run behaves. That is what keeps it safe to write after the run has
// already been scored.

// EVAL_RUNS_DIR is where run records are written, relative to the repo root
// (where `go run . eval` executes). Git-ignored: these are local run history,
// regenerable only by spending money again, so they are neither source of truth
// nor something to review in a diff. A mutable package var so tests can point it
// at t.TempDir().
var EVAL_RUNS_DIR = "evalRuns"

// RUN_ID_TIME_FORMAT stamps the run's UTC start time into its ID. Basic RFC3339
// (no colons) because the ID is also a filename: colons are illegal on Windows
// and awkward to quote in a shell. Lexical order is chronological order, so the
// folder sorts by time for free.
const RUN_ID_TIME_FORMAT = "20060102T150405Z"

// DI seams (the repo-wide function-variable convention): tests swap these so a
// record can be built without shelling out to git or depending on wall time.
var (
	now    = time.Now
	gitSHA = headCommitSHA
)

// RunMetrics is the metrics block of a run record: the confusion matrix plus the
// two rates. It deliberately mirrors evalHarness.Metrics MINUS the false
// positive/negative title lists - those are per-story detail, and per-story
// detail belongs in the run's CSV (one row per dataset item), not duplicated
// here in a shape that only holds the mistakes.
type RunMetrics struct {
	TP        int     `json:"tp"`
	FP        int     `json:"fp"`
	TN        int     `json:"tn"`
	FN        int     `json:"fn"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
}

// RunRecord is the artifact written to evalRuns/<run_id>.json after every eval.
//
// The point of each field is to pin one thing that can change the numbers:
//
//   - Arm      - which classification flow ran.
//   - Config   - the arm's tuning constants, by name and value. These used to be
//     pinned only by GitSHA, which over-pinned them: any commit at all changed
//     the sha, so two runs with identical tuning stopped being comparable the
//     moment an unrelated commit landed between them (issue #40).
//   - GitSHA   - the commit, and so everything about the code that Config does
//     NOT name - prompt wording above all. Kept as provenance, and no longer
//     the grouping key; see fingerprint in report_group.go.
//   - Model    - the LLM that classified. Changing it re-bases every number.
//   - RerankModel - the cross-encoder that ranked the excerpts (arm 4 only).
//     Changing it re-bases every arm-4 number, the same way Model does.
//   - DatasetHash / CorpusIndexHash - the data, which is git-ignored and mutable
//     in place. Each hash names an archived copy under evalRuns/, so the bytes
//     behind a past number are still recoverable. See artifacts.go.
//
// CorpusIndexHash and RerankModel are omitted entirely for the arms that do not
// use them: arms 0 and 1 never load the index, arms 0-3 never rerank, and an
// empty string would read as "the index was empty" / "an empty rerank model was
// used" rather than "this was irrelevant here".
//
// RerankModel is worth recording even though voyageapi.VOYAGE_RERANK_MODEL is a
// compile-time const rather than a git-ignored data file - so for a COMMITTED
// change GitSHA already splits the two runs into different groups. It earns its
// place the same way Model does (equally const-pinned, equally recorded): a
// reader can see which cross-encoder ranked the excerpts without checking out
// the commit, and the grouping key stops implying the reranker was held
// constant across arm-4 runs.
//
// Config is omitempty too, but its absence means something different from the
// other two: not "irrelevant to this arm" but "written before issue #40". Every
// arm has a config. See fingerprint in report_group.go for how those records are
// grouped, since they cannot be back-filled.
type RunRecord struct {
	RunID           string            `json:"run_id"`
	Arm             int               `json:"arm"`
	GitSHA          string            `json:"git_sha"`
	DatasetHash     string            `json:"dataset_hash"`
	CorpusIndexHash string            `json:"corpus_index_hash,omitempty"`
	Model           string            `json:"model"`
	RerankModel     string            `json:"rerank_model,omitempty"`
	Config          map[string]string `json:"config,omitempty"`
	Metrics         RunMetrics        `json:"metrics"`
}

// configForArm returns the tuning constants that decide the given arm's numbers,
// as name -> value.
//
// ⚠️ Like armUsesReranker and armUsesCorpusIndex, it mirrors
// armcontext.BuildProfile's cases and must be updated when an arm is added or
// when an arm starts reading a new constant. An arm missing a constant here
// records a config that does not describe it, and two runs that differed in that
// constant would group as repeats of one experiment - reporting a real
// difference as noise, which is the failure grouping exists to prevent.
//
// Values are formatted to string HERE, once. The map is the canonical form that
// gets hashed (see fingerprint), so its formatting decides group identity:
// re-deriving "0.3330" as "0.333" somewhere else would split a group for a
// formatting reason. 'f', -1 is Go's shortest round-trip form, so the value
// reads as it does in source.
//
// EVAL_BATCH_SIZE is in every arm's config despite being a harness setting
// rather than an arm knob: it decides how many stories ride in one Claude call,
// which every arm's numbers depend on. The rule is "what decides this arm's
// numbers", not "what lives in rag".
func configForArm(arm hackernews_classifier.Arm) map[string]string {
	config := map[string]string{
		"eval_batch_size": strconv.Itoa(EVAL_BATCH_SIZE),
	}

	switch arm {
	case hackernews_classifier.ArmRAG:
		config["retrieval_top_k"] = strconv.Itoa(rag.RETRIEVAL_TOP_K)

	case hackernews_classifier.ArmRAGPerStory:
		config["retrieval_top_k_per_story"] = strconv.Itoa(rag.RETRIEVAL_TOP_K_PER_STORY)
		config["retrieval_similarity_floor"] = strconv.FormatFloat(rag.RETRIEVAL_SIMILARITY_FLOOR, 'f', -1, 64)
		config["retrieval_pool_cap"] = strconv.Itoa(rag.RETRIEVAL_POOL_CAP)

	case hackernews_classifier.ArmRerank:
		// Arm 4 selects candidates by RANK, so it reads neither
		// RETRIEVAL_TOP_K_PER_STORY nor RETRIEVAL_SIMILARITY_FLOOR - recording
		// them would claim an input it never consulted. See rag.rerank.go.
		config["rerank_candidates_per_story"] = strconv.Itoa(rag.RERANK_CANDIDATES_PER_STORY)
		config["rerank_candidate_floor"] = strconv.FormatFloat(rag.RERANK_CANDIDATE_FLOOR, 'f', -1, 64)
		config["rerank_top_k_per_story"] = strconv.Itoa(rag.RERANK_TOP_K_PER_STORY)
		config["retrieval_pool_cap"] = strconv.Itoa(rag.RETRIEVAL_POOL_CAP)
	}

	return config
}

// armUsesReranker reports whether an arm ranks its excerpts with a
// cross-encoder, and so whether its record should name one.
//
// ⚠️ Like armUsesCorpusIndex, it mirrors armcontext.BuildProfile's cases and
// must be updated alongside them: an arm that reranks but is missing here would
// publish numbers whose ranking side is untraceable. It lives here rather than
// beside armUsesCorpusIndex in artifacts.go because nothing is archived and no
// file is hashed for it - it gates a struct field, not file I/O.
func armUsesReranker(arm hackernews_classifier.Arm) bool {
	return arm == hackernews_classifier.ArmRerank
}

// newRunID builds the run's identity: "<UTC timestamp>-arm-<N>". It is both the
// record's ID and the stem of its filename, so a file can be traced to a run and
// back without opening it.
//
// The .UTC() belongs here rather than in the `now` seam: the ID's timezone is a
// property of the ID, not of whichever clock happens to be installed. Putting it
// in the seam would mean a run in a non-UTC zone stamps local time and IDs from
// two machines sort against each other wrongly.
func newRunID(arm hackernews_classifier.Arm) string {
	return fmt.Sprintf("%s-arm-%d", now().UTC().Format(RUN_ID_TIME_FORMAT), int(arm))
}

// headCommitSHA returns the full SHA of HEAD.
//
// ⚠️ It reports the last COMMIT, not the working tree. An uncommitted edit to a
// tuning constant is invisible here, so a record can claim a commit whose code
// is not what ran. Accepted deliberately (issue #26) to keep the record to its
// specified fields; the discipline it implies is to commit tuning changes before
// measuring them.
func headCommitSHA() (string, error) {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("failed to read git HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// buildRunRecord assembles the record from the run's identity, the hashes of the
// data it read, and its scored metrics. Pure apart from the gitSHA seam, so the
// mapping is unit-testable.
func buildRunRecord(runID string, arm hackernews_classifier.Arm, artifacts runArtifacts, metrics Metrics) (RunRecord, error) {
	sha, err := gitSHA()
	if err != nil {
		return RunRecord{}, err
	}

	rerankModel := ""
	if armUsesReranker(arm) {
		rerankModel = voyageapi.VOYAGE_RERANK_MODEL
	}

	return RunRecord{
		RunID:           runID,
		Arm:             int(arm),
		GitSHA:          sha,
		DatasetHash:     artifacts.DatasetHash,
		CorpusIndexHash: artifacts.CorpusIndexHash,
		Model:           claudeapi.ANTHROPIC_MODEL_NAME,
		RerankModel:     rerankModel,
		Config:          configForArm(arm),
		Metrics: RunMetrics{
			TP:        metrics.Confusion.TP,
			FP:        metrics.Confusion.FP,
			TN:        metrics.Confusion.TN,
			FN:        metrics.Confusion.FN,
			Precision: metrics.Precision,
			Recall:    metrics.Recall,
		},
	}, nil
}

// writeRunRecord persists the record to evalRuns/<run_id>.json, creating the
// directory if needed.
func writeRunRecord(record RunRecord) error {
	if err := os.MkdirAll(EVAL_RUNS_DIR, 0755); err != nil {
		return fmt.Errorf("failed to create %s: %w", EVAL_RUNS_DIR, err)
	}

	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal run record: %w", err)
	}

	path := filepath.Join(EVAL_RUNS_DIR, record.RunID+".json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write run record to %s: %w", path, err)
	}
	return nil
}
