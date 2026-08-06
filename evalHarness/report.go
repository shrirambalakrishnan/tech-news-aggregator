package evalHarness

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
)

// This file is the OUTPUT stage of the eval-run aggregator (issue #34): it turns
// the records loaded by report_load.go into tables on the console.
//
// `go run . eval <arm>` prints the metrics of the run it just paid for and
// nothing else. The history it leaves behind under evalRuns/ has, until now, been
// read only by hand - which is how CLAUDE.md ended up quoting a run-to-run spread
// someone recomputed with a calculator, and how README's results table needs
// prose footnotes to say which rows were measured against which corpus.
//
// This command reads that history and prints it. It costs $0.00, makes no API
// call, and WRITES NOTHING - it is a view over the records, never an input to
// them, so running it can't change what a future eval means.

// Short-hash widths. git_sha is 7, git's own abbreviation length, so the value is
// pastable straight into `git show`. The content hashes are 12 of the sha256,
// enough that `ls evalRuns/corpus_index/<prefix>*` names exactly one file.
const (
	GIT_SHA_SHORT_LEN      = 7
	CONTENT_HASH_SHORT_LEN = 12
)

// ABSENT_VALUE marks a field that has no value, as opposed to a zero one. Arms 0
// and 1 record no corpus_index_hash because they never load the index; rendering
// that as blank or as 0 would read as "an empty index was used". Same rule as the
// n=1 no-± convention in report_group.go: an absent measurement must never look
// like a measured one.
const ABSENT_VALUE = "—"

// loadRecords is the DI seam (the repo-wide function-variable convention), so
// the rendering tests never touch disk.
var loadRecords = loadRunRecords

// RunEvalReport is the `go run . eval-report` entrypoint: read every run record
// under evalRuns/ and print two tables - one row per run, then one row per
// distinct configuration with its mean and spread.
//
// Free and read-only. Nothing here calls Claude or Voyage, and nothing is
// written, so it can be run as often as the question comes up.
func RunEvalReport() error {
	return renderEvalReport(os.Stdout, os.Stderr)
}

// renderEvalReport is RunEvalReport with its two streams injected, matching
// calibrateFloor's shape, so the whole flow is testable without writing to the
// console. Tables go to out; warnings about unusable record files go to warnOut,
// so `go run . eval-report > report.txt` keeps the report clean while the
// operator still sees what was skipped.
func renderEvalReport(out, warnOut io.Writer) error {
	records, skipped := loadRecords(EVAL_RUNS_DIR)

	for _, err := range skipped {
		fmt.Fprintf(warnOut, "eval-report: %v\n", err)
	}

	if len(records) == 0 {
		fmt.Fprintf(out, "\n%s\n\n", runsHeader(0, len(skipped)))
		fmt.Fprintf(out, "No eval runs recorded yet — run `go run . eval <arm>` first.\n\n")
		return nil
	}

	fmt.Fprintf(out, "\n%s\n\n", runsHeader(len(records), len(skipped)))
	fmt.Fprint(out, formatRunsTable(records))

	return nil
}

// runsHeader names how many runs the table covers AND how many files could not
// be read. The skip count belongs in the header rather than only on stderr: a
// skipped record makes the history incomplete, and a reader looking at the table
// later (or at a redirected copy of it) must not have to trust that no warning
// scrolled past.
func runsHeader(runs, skipped int) string {
	if skipped == 0 {
		return fmt.Sprintf("=== Eval runs (%d) ===", runs)
	}
	return fmt.Sprintf("=== Eval runs (%d, %d skipped) ===", runs, skipped)
}

// formatRunsTable renders one row per run: what it was (arm, code, data, model)
// beside what it scored. Sorted by run_id, which loadRunRecords already
// guarantees is chronological.
func formatRunsTable(records []RunRecord) string {
	var b strings.Builder
	w := newTableWriter(&b)

	fmt.Fprintln(w, "run_id\tarm\tgit_sha\tdataset_hash\tcorpus_index\tmodel\tTP\tFP\tTN\tFN\tprecision\trecall")
	for _, r := range records {
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%.4f\t%.4f\n",
			r.RunID,
			r.Arm,
			shortHash(r.GitSHA, GIT_SHA_SHORT_LEN),
			shortHash(r.DatasetHash, CONTENT_HASH_SHORT_LEN),
			shortHash(r.CorpusIndexHash, CONTENT_HASH_SHORT_LEN),
			r.Model,
			r.Metrics.TP, r.Metrics.FP, r.Metrics.TN, r.Metrics.FN,
			r.Metrics.Precision, r.Metrics.Recall,
		)
	}

	w.Flush()
	return b.String()
}

// newTableWriter is the one place the table geometry is set, so both tables line
// up with each other. Two spaces of padding, no padding character tricks: the
// output is meant to be read in a terminal and pasted into an issue.
func newTableWriter(b *strings.Builder) *tabwriter.Writer {
	return tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
}

// shortHash abbreviates a hash to n characters, and renders an absent one as
// ABSENT_VALUE rather than as an empty cell. Safe on strings shorter than n:
// callers pass whatever a record happens to hold, and a record written by an
// older version is not worth panicking over.
func shortHash(s string, n int) string {
	if s == "" {
		return ABSENT_VALUE
	}
	if len(s) <= n {
		return s
	}
	return s[:n]
}
