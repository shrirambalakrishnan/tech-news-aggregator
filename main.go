package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
	"github.com/shrirambalakrishnan/tech-news/evalHarness"
	"github.com/shrirambalakrishnan/tech-news/hackernews_classifier"
	"github.com/shrirambalakrishnan/tech-news/profile"
	"github.com/shrirambalakrishnan/tech-news/rag"
)

// DI seam (the repo-wide function-variable convention): lets the dispatch test
// assert that `eval-report` reaches the aggregator without the test rendering the
// real evalRuns/ directory to the console.
var runEvalReportCommand = evalHarness.RunEvalReport

// parseArm converts a CLI argument into an Arm. Only the valid arms (0-4) are
// accepted; anything else errors so a typo can't silently fall through to a
// default flow.
func parseArm(s string) (hackernews_classifier.Arm, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid arm %q: must be an integer", s)
	}
	switch hackernews_classifier.Arm(n) {
	case hackernews_classifier.ArmGeneric, hackernews_classifier.ArmInterests,
		hackernews_classifier.ArmRAG, hackernews_classifier.ArmRAGPerStory,
		hackernews_classifier.ArmRerank:
		return hackernews_classifier.Arm(n), nil
	default:
		return 0, fmt.Errorf("unknown arm: %d (valid: 0=generic, 1=interests, 2=RAG, 3=RAG per-story, 4=RAG per-story reranked)", n)
	}
}

// main is deliberately only error plumbing: all the work (and every failure
// path) lives in run, which returns an error rather than calling log.Fatal deep
// in the logic, so there is exactly one exit point.
func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

// run dispatches on the subcommand. args is os.Args WITHOUT the program name,
// so args[0] is the subcommand (or the arm, for a normal run).
//
//	go run . [arm]      -> classify (arm optional, defaults to 0)
//	go run . eval <arm> -> eval     (arm REQUIRED)
//	go run . eval-report -> summarise the eval runs recorded so far
//	go run . prebuild   -> prebuild
//	go run . embed      -> build the corpus index the retrieval arms (2, 3, 4) read
//	go run . calibrate-floor -> pick rag.RETRIEVAL_SIMILARITY_FLOOR from measured scores
func run(args []string) error {
	godotenv.Load()

	command := ""
	if len(args) > 0 {
		command = args[0]
	}

	switch command {
	case "prebuild":
		return runPrebuild()
	case "embed":
		return runEmbed()
	case "calibrate-floor":
		return runCalibrateFloor()
	case "eval":
		return runEval(args[1:])
	case "eval-report":
		return runEvalReport(args[1:])
	default:
		// Not a known subcommand, so it's a normal run and args[0] - if present
		// at all - is the arm.
		return runClassify(args)
	}
}

// runPrebuild fetches GitHub READMEs, extracts the user's interests via the LLM,
// and writes profile/user_context.json. Run occasionally, not every 4h.
func runPrebuild() error {
	profile.ExtractGithubProfile()
	return nil
}

// runEmbed chunks profile/corpus, embeds each chunk via Voyage, and writes
// profile/corpus_index.json - the index the retrieval arms (2, 3 and 4) read.
// Run occasionally, not every 4h; requires VOYAGE_API_KEY in env.
//
// It then archives the index under evalRuns/ (issue #26). The eval flow archives
// it too, so this is not what makes an index traceable - it is what makes it
// traceable EARLY: an index built today and first evaluated next week would
// otherwise be captured only at eval time, and any `embed` in between overwrites
// it in place with no snapshot taken.
//
// ⚠️ rag.BuildCorpusIndex fails soft (it logs and returns nothing), so this
// cannot tell a successful build from a failed one. On a failed build it
// archives whichever index is still on disk - which is a truthful record of what
// the RAG arms would read right now, just not evidence that this run produced
// it. Making BuildCorpusIndex return an error would close that gap and is worth
// doing; it is deliberately not bundled into the tracking change.
func runEmbed() error {
	rag.BuildCorpusIndex()
	return evalHarness.ArchiveCorpusIndex()
}

// runCalibrateFloor picks the value of rag.RETRIEVAL_SIMILARITY_FLOOR.
//
// That floor is a cosine-similarity threshold (a score in [-1, 1], NOT a count
// of chunks): arm 3 ignores corpus chunks scoring below it, so a story with no
// real corpus support contributes no excerpts at all.
//
// Choosing it by trial and error is impractical because cosine scores cluster in
// a narrow band that differs per embedding model. Every floor below that band
// filters nothing and every floor above it filters everything, so most values
// you could try produce the identical result - while each try costs a full eval
// run (~$0.02, ~10 min, and noisy enough at 35 positives to need repeats).
//
// So instead: score every labelled title against the corpus, split the scores by
// the human label, and look at where the relevant and irrelevant stories
// separate. The floor goes in that gap. If they don't separate, no floor works -
// which is worth learning here rather than after an afternoon of eval runs.
//
// The report ends with a recommended value and the reason for it, so the numbers
// don't have to be interpreted by hand. It also checks the floor against
// rag.RETRIEVAL_POOL_CAP, which truncates the pooled chunks and so acts as a
// floor of its own: a floor below what the cap already enforces is inert, and
// that reads very differently from a floor that fired and didn't help.
//
// Re-run after every `embed`: the scores depend on the corpus, so the right
// floor does too. Free - Voyage embeddings only, no Claude call.
//
//	go run . calibrate-floor > scores.csv   # CSV on stdout, report on stderr
func runCalibrateFloor() error {
	return evalHarness.RunFloorCalibration()
}

// runEval runs the classifier over the hand-labelled dataset and prints
// precision/recall plus the misclassified titles. Offline quality check, not
// part of the scheduled run. The arm is REQUIRED (no default) so an eval meant
// for one arm can't silently score another.
//
// It also leaves a record of the run under evalRuns/ (issue #26). Note the
// error it returns may arrive AFTER a successful eval printed its report - a
// failed recording is worth a non-zero exit, but not worth suppressing numbers
// that have already been paid for.
func runEval(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("eval requires an arm: `go run . eval <arm>` (0=generic, 1=interests, 2=RAG, 3=RAG per-story, 4=RAG per-story reranked)")
	}
	arm, err := parseArm(args[0])
	if err != nil {
		return fmt.Errorf("eval: %w", err)
	}
	return evalHarness.RunEval(arm)
}

// runEvalReport summarises every eval run recorded under evalRuns/ so far: one
// table listing each run, then one grouping the runs that shared an arm, a
// commit, a model and the same data, with the mean and spread of their metrics.
//
// It exists because a single eval run is a point estimate. The classifier is
// non-deterministic (claudeapi sends no temperature, so the API default applies),
// so the question "is arm X better than arm Y" is really "is the gap bigger than
// the run-to-run spread" - and answering it meant opening record files by hand
// and reaching for a calculator.
//
// Costs nothing and WRITES nothing: no Claude call, no Voyage call, no file
// touched. It is a view over the records, never an input to them.
func runEvalReport(args []string) error {
	// Strict about extra arguments, like parseArm: a mistyped flag must fail
	// loudly rather than be silently ignored, or the operator will believe they
	// filtered a report that was in fact unfiltered.
	if len(args) > 0 {
		return fmt.Errorf("eval-report takes no arguments, got %q", args[0])
	}
	return runEvalReportCommand()
}

// runClassify is the scheduled path: fetch the HN front page, classify under the
// chosen arm, append the matches to stories.md. The arm defaults to ArmGeneric
// so the unattended production run (bare `go run .` from tech-news-run.sh) stays
// cron-safe.
func runClassify(args []string) error {
	arm := hackernews_classifier.ArmGeneric
	if len(args) > 0 {
		parsed, err := parseArm(args[0])
		if err != nil {
			return err
		}
		arm = parsed
	}

	stories, err := GetMyHackerNewsStories(arm)
	if err != nil {
		return err
	}
	log.Println("stories = ", stories)

	if err := appendStoriesToMarkdown(stories); err != nil {
		return fmt.Errorf("failed to write markdown: %w", err)
	}
	return nil
}

func appendStoriesToMarkdown(stories []HackerNewsStory) error {
	f, err := os.OpenFile("stories.md", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	timestamp := time.Now().Format("02-01-2006 @ 15:04")
	fmt.Fprintf(f, "\n## %s\n\n", timestamp)

	for _, story := range stories {
		hnURL := "https://news.ycombinator.com/item?id=" + story.ObjectID
		fmt.Fprintf(f, "- [%s](%s)\n", story.Title, hnURL)
	}

	return nil
}
