package evalHarness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// This file is the INPUT stage of the eval-run aggregator (issue #34): it reads
// the records runrecord.go wrote back off disk.
//
// Every eval run leaves a record behind, but nothing has ever read one. The
// numbers in README were compared by opening files by hand, and the run-to-run
// spread quoted in CLAUDE.md (Approach 5's "0.343 ± 0.076") was computed by hand
// from three of them. Reading them back is the first half of doing that
// automatically; report_group.go is the second.
//
// Loading is DESCRIPTIVE, exactly as writing is: nothing here changes how a run
// behaves, so this can never make an eval mean something different.

// loadRunRecords reads every run record in dir and returns them sorted by run
// ID, plus one error per file that could not be used.
//
// It returns the skip errors rather than logging them, so the caller decides how
// loudly to report them. That matters because the aggregator prints the skip
// COUNT into the table header, not just into a log line - see below.
//
// ⚠️ Skipping is a deliberate exception to the repo's no-fail-soft rule (issue
// #11): one unreadable record should not deny the operator a report over the
// other twenty. The risk it carries is that `n` - the number the whole variance
// table hangs on - could silently undercount, so the caller must surface the skip
// count somewhere the reader of the table will see it.
//
// The glob is NON-RECURSIVE on purpose. The archived artifacts under
// evalRuns/datasets/ and evalRuns/corpus_index/ are also named <...>.json; a
// recursive walk would try to parse a 4.4 MB corpus index as a run record and
// turn every archived snapshot into a warning. Non-recursive means they are never
// looked at.
func loadRunRecords(dir string) ([]RunRecord, []error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, []error{fmt.Errorf("failed to list run records in %s: %w", dir, err)}
	}

	var records []RunRecord
	var skipped []error

	for _, path := range paths {
		record, err := readRunRecord(path)
		if err != nil {
			skipped = append(skipped, err)
			continue
		}
		records = append(records, record)
	}

	// Run IDs are timestamp-prefixed (RUN_ID_TIME_FORMAT), so lexical order is
	// chronological order - the table reads as the run history it is.
	sort.Slice(records, func(i, j int) bool { return records[i].RunID < records[j].RunID })

	return records, skipped
}

// readRunRecord parses one record file.
//
// The empty-RunID check is not a formality: encoding/json ignores unknown fields
// and leaves absent ones zeroed, so ANY JSON object decodes into a RunRecord
// without complaint. Without this check a stray JSON file would land in the table
// as an arm-0 run that scored nothing - a fabricated row, which is worse than a
// skipped one. Every real record has a run ID, since the ID is also its filename.
func readRunRecord(path string) (RunRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RunRecord{}, fmt.Errorf("skipped %s: %w", path, err)
	}

	var record RunRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return RunRecord{}, fmt.Errorf("skipped %s: not a valid run record: %w", path, err)
	}
	if record.RunID == "" {
		return RunRecord{}, fmt.Errorf("skipped %s: not a run record (no run_id)", path)
	}

	return record, nil
}
