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

	groups := groupRuns(records)
	fmt.Fprintf(out, "\n%s\n\n", groupsHeader(len(groups)))
	fmt.Fprint(out, formatGroupsTable(groups))
	fmt.Fprint(out, "\n"+GROUPS_TABLE_FOOTNOTE)

	renderStabilitySections(out, warnOut, groups)

	return nil
}

// STABILITY_MIN_RUNS is the smallest group the stability block is printed for.
//
// One run measures no stability at all: every story it flagged was flagged by
// every run, so the "sometimes" column would be structurally zero and the block
// would read as perfect consistency. Same rule, and the same failure it avoids,
// as the n=1 no-± convention in report_group.go.
const STABILITY_MIN_RUNS = 2

// renderStabilitySections prints one per-story stability block per group with
// enough runs to have something to say, in Output 2's own order - groupRuns
// already imposes a total ordering, so reusing its slice makes this section
// deterministic for free.
//
// ⚠️ A group whose CSVs cannot be read is SKIPPED with a warning, not fatal -
// the same posture loadRunRecords takes toward an unreadable record, and for the
// same reason: one missing file should not deny the operator the rest of a
// report that is free to produce and already correct.
func renderStabilitySections(out, warnOut io.Writer, groups []runGroup) {
	for _, g := range groups {
		if g.N < STABILITY_MIN_RUNS {
			continue
		}

		table, err := stabilityForGroup(g)
		if err != nil {
			fmt.Fprintf(warnOut, "eval-report: no stability block for arm %d / %s: %v\n",
				g.Key.Arm, shortHash(g.Key.GitSHA, GIT_SHA_SHORT_LEN), err)
			continue
		}

		fmt.Fprintf(out, "\n%s\n\n", stabilityHeader(g))
		fmt.Fprint(out, formatStabilityTable(table))
	}
}

// stabilityForGroup loads every member run's predictions and buckets them.
//
// All-or-nothing per group: a partial block computed from some of the runs would
// carry a header saying n=3 over counts measured across 2, which is worse than
// no block at all.
func stabilityForGroup(g runGroup) (stabilityTable, error) {
	runs := make([][]storyVerdict, 0, len(g.RunIDs))
	for _, runID := range g.RunIDs {
		verdicts, err := loadPredictions(EVAL_RUNS_DIR, runID)
		if err != nil {
			return stabilityTable{}, err
		}
		runs = append(runs, verdicts)
	}
	return bucketStability(runs), nil
}

// stabilityHeader names the configuration in full, over two lines so it fits a
// terminal. Neither model is abbreviated, for the same reason they are not in the
// groups table: both are grouping keys, and truncating one could make two
// different models look like one configuration. The rerank model is absent for
// every arm but 4, and renders as ABSENT_VALUE rather than as a blank.
func stabilityHeader(g runGroup) string {
	return fmt.Sprintf(
		"=== Per-story stability — arm %d, %s, dataset %s,\n     index %s, %s, rerank %s (n=%d runs) ===",
		g.Key.Arm,
		shortHash(g.Key.GitSHA, GIT_SHA_SHORT_LEN),
		shortHash(g.Key.DatasetHash, CONTENT_HASH_SHORT_LEN),
		shortHash(g.Key.CorpusIndexHash, CONTENT_HASH_SHORT_LEN),
		g.Key.Model,
		orAbsent(g.Key.RerankModel),
		g.N,
	)
}

// formatStabilityTable renders the two rows and three columns, and nothing else.
// No title lists and no combining-rule scores: issue #37 parked both, and the
// value of this block is that six numbers answer the question.
//
// Each row label carries its own total, so a reader can see at a glance what the
// three counts are a share of without adding them up.
func formatStabilityTable(table stabilityTable) string {
	var b strings.Builder
	w := newTableWriter(&b)

	fmt.Fprintln(w, "\tnever\tsometimes\talways")
	fmt.Fprintf(w, "\t%s\t%s\t%s\n",
		runCountLabel(0, 0, table.N),
		runCountLabel(1, table.N-1, table.N),
		runCountLabel(table.N, table.N, table.N),
	)
	fmt.Fprintf(w, "relevant (%d)\t%d\t%d\t%d\n",
		table.Relevant.Total(), table.Relevant.Never, table.Relevant.Sometimes, table.Relevant.Always)
	fmt.Fprintf(w, "irrelevant (%d)\t%d\t%d\t%d\n",
		table.Irrelevant.Total(), table.Irrelevant.Never, table.Irrelevant.Sometimes, table.Irrelevant.Always)

	w.Flush()
	return b.String()
}

// runCountLabel states a column's range of run counts: "(0 of 2)", "(1 of 2)",
// "(1-2 of 3)".
//
// The counts are spelled out rather than left implicit because "sometimes" is
// a far weaker claim at n=2, where it can only mean 1 of 2, than at n=7. A
// reader must be able to see which they are looking at without going to the
// source.
func runCountLabel(low, high, n int) string {
	if low == high {
		return fmt.Sprintf("(%d of %d)", low, n)
	}
	return fmt.Sprintf("(%d-%d of %d)", low, high, n)
}

// GROUPS_TABLE_FOOTNOTE states what the second table cannot show, next to the
// table rather than in a document nobody has open. Both caveats are inherited
// from issue #26's run record and cannot be detected here: the aggregator can
// only group by what was recorded.
const GROUPS_TABLE_FOOTNOTE = "" +
	"n=1 groups show no ± — one run measures no spread.\n" +
	"git_sha is HEAD, not the working tree: two runs sharing a sha may have run\n" +
	"different uncommitted code, and would be grouped here as one configuration.\n" +
	"Arm 1's profile/user_context.json is not hashed, so arm-1 rows group on a key\n" +
	"that omits an input deciding their numbers.\n"

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

	fmt.Fprintln(w, "run_id\tarm\tgit_sha\tdataset_hash\tcorpus_index\tmodel\trerank_model\tTP\tFP\tTN\tFN\tprecision\trecall")
	for _, r := range records {
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%.4f\t%.4f\n",
			r.RunID,
			r.Arm,
			shortHash(r.GitSHA, GIT_SHA_SHORT_LEN),
			shortHash(r.DatasetHash, CONTENT_HASH_SHORT_LEN),
			shortHash(r.CorpusIndexHash, CONTENT_HASH_SHORT_LEN),
			r.Model,
			orAbsent(r.RerankModel),
			r.Metrics.TP, r.Metrics.FP, r.Metrics.TN, r.Metrics.FN,
			r.Metrics.Precision, r.Metrics.Recall,
		)
	}

	w.Flush()
	return b.String()
}

// groupsHeader names the grouping tuple in full, so the table is self-describing
// - a reader must be able to tell what "the same configuration" meant here
// without going to the source, since that definition is the whole basis for
// calling two runs repeats of one experiment.
func groupsHeader(groups int) string {
	noun := "groups"
	if groups == 1 {
		noun = "group"
	}
	return fmt.Sprintf(
		"=== Grouped by (arm, git_sha, model, rerank_model, dataset_hash, corpus_index_hash) — %d %s ===",
		groups, noun,
	)
}

// formatGroupsTable renders one row per configuration: how many runs measured it,
// their mean confusion matrix, and their precision/recall with spread.
//
// Mean counts print to one decimal - they are averages of integers, and a mean
// of 22.5 TP must not round to 22 and read as a count that was observed. The
// rates print to four, matching Metrics.Report(), so a row here is directly
// comparable to the report of the run it came from.
func formatGroupsTable(groups []runGroup) string {
	var b strings.Builder
	w := newTableWriter(&b)

	fmt.Fprintln(w, "arm\tgit_sha\tdataset_hash\tcorpus_index\tmodel\trerank_model\tn\tTP\tFP\tTN\tFN\tprecision\trecall")
	for _, g := range groups {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%d\t%.1f\t%.1f\t%.1f\t%.1f\t%s\t%s\n",
			g.Key.Arm,
			shortHash(g.Key.GitSHA, GIT_SHA_SHORT_LEN),
			shortHash(g.Key.DatasetHash, CONTENT_HASH_SHORT_LEN),
			shortHash(g.Key.CorpusIndexHash, CONTENT_HASH_SHORT_LEN),
			g.Key.Model,
			orAbsent(g.Key.RerankModel),
			g.N,
			g.MeanTP, g.MeanFP, g.MeanTN, g.MeanFN,
			formatAggregate(g.Precision),
			formatAggregate(g.Recall),
		)
	}

	w.Flush()
	return b.String()
}

// formatAggregate renders "0.2184 ± 0.0206" when the spread was measured and a
// bare "0.2184" when it was not.
//
// ⚠️ It must never print "± 0.0000". At n=1 there is no spread to report, and a
// zero there reads as perfect reproducibility - the opposite of the truth, which
// is that nothing is known about it. One function for both metric columns, so the
// rule holds by construction rather than by two call sites remembering it.
func formatAggregate(a aggregate) string {
	if !a.StdDevDefined {
		return fmt.Sprintf("%.4f", a.Mean)
	}
	return fmt.Sprintf("%.4f ± %.4f", a.Mean, a.StdDev)
}

// newTableWriter is the one place the table geometry is set, so both tables line
// up with each other. Two spaces of padding, no padding character tricks: the
// output is meant to be read in a terminal and pasted into an issue.
func newTableWriter(b *strings.Builder) *tabwriter.Writer {
	return tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
}

// orAbsent renders a field that has no value as ABSENT_VALUE, leaving anything
// else untouched.
//
// It is deliberately NOT shortHash with a large width: shortHash truncates, and
// rerank-2.5-lite is 15 characters against the 12-character hash width, so
// reusing it would print "rerank-2.5-l" - a model id Voyage does not have, with
// the exact suffix that separates it from rerank-2.5 cut off. None of Voyage's
// six rerank ids actually collide at 12 characters, so the failure is a mangled
// value rather than an ambiguous one; a grouping key must render as the thing it
// keys on either way.
func orAbsent(s string) string {
	if s == "" {
		return ABSENT_VALUE
	}
	return s
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
