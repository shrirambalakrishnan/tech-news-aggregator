package main

import (
	"strings"
	"testing"
)

// withStubbedEvalReport swaps the aggregator seam so the dispatch test does not
// render the real evalRuns/ directory to the console.
func withStubbedEvalReport(t *testing.T, called *bool) {
	t.Helper()

	original := runEvalReportCommand
	runEvalReportCommand = func() error {
		*called = true
		return nil
	}
	t.Cleanup(func() { runEvalReportCommand = original })
}

func TestRunDispatchesEvalReport(t *testing.T) {
	var called bool
	withStubbedEvalReport(t, &called)

	if err := run([]string{"eval-report"}); err != nil {
		t.Fatalf("run(eval-report) returned error: %v", err)
	}
	if !called {
		t.Error("run(eval-report) did not reach the aggregator")
	}
}

// Strict about extra arguments, like parseArm: a mistyped flag must fail loudly
// rather than be ignored, or the operator will believe they filtered a report
// that was in fact unfiltered.
func TestRunEvalReportRejectsExtraArguments(t *testing.T) {
	var called bool
	withStubbedEvalReport(t, &called)

	err := run([]string{"eval-report", "--arm=2"})
	if err == nil {
		t.Fatal("expected an error for an extra argument, got nil")
	}
	if !strings.Contains(err.Error(), "eval-report") {
		t.Errorf("error should name the command, got: %v", err)
	}
	if called {
		t.Error("aggregator ran despite the bad arguments")
	}
}

// `eval-report` must not be mistaken for `eval` with an arm - it spends nothing,
// while `eval` spends money, so the two commands must stay distinct in the
// switch.
func TestEvalReportIsNotTreatedAsAnArm(t *testing.T) {
	if _, err := parseArm("eval-report"); err == nil {
		t.Error("parseArm accepted \"eval-report\" as an arm")
	}
}
