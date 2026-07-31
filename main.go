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

// parseArm converts a CLI argument into an Arm. Only the valid arms (0, 1, 2)
// are accepted; anything else errors so a typo can't silently fall through to a
// default flow.
func parseArm(s string) (hackernews_classifier.Arm, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid arm %q: must be an integer", s)
	}
	switch hackernews_classifier.Arm(n) {
	case hackernews_classifier.ArmGeneric, hackernews_classifier.ArmInterests, hackernews_classifier.ArmRAG:
		return hackernews_classifier.Arm(n), nil
	default:
		return 0, fmt.Errorf("unknown arm: %d (valid: 0=generic, 1=interests, 2=RAG)", n)
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
//	go run . prebuild   -> prebuild
//	go run . embed      -> build the arm 2 (RAG) corpus index
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
	case "eval":
		return runEval(args[1:])
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
// profile/corpus_index.json - the index arm 2 (RAG) retrieves from. Run
// occasionally, not every 4h; requires VOYAGE_API_KEY in env.
func runEmbed() error {
	rag.BuildCorpusIndex()
	return nil
}

// runEval runs the classifier over the hand-labelled dataset and prints
// precision/recall plus the misclassified titles. Offline quality check, not
// part of the scheduled run. The arm is REQUIRED (no default) so an eval meant
// for one arm can't silently score another.
func runEval(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("eval requires an arm: `go run . eval <arm>` (0=generic, 1=interests, 2=RAG)")
	}
	arm, err := parseArm(args[0])
	if err != nil {
		return fmt.Errorf("eval: %w", err)
	}
	evalHarness.RunEval(arm)
	return nil
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
