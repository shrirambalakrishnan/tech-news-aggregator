package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/joho/godotenv"
	"github.com/shrirambalakrishnan/tech-news/evalHarness"
	"github.com/shrirambalakrishnan/tech-news/profile"
	"github.com/shrirambalakrishnan/tech-news/rag"
)

func main() {

	godotenv.Load()

	// Prebuild step: fetch GitHub READMEs, extract the user's interests via the
	// LLM, and write profile/user_context.json. Run occasionally (not every 4h):
	//   go run . prebuild
	if len(os.Args) > 1 && os.Args[1] == "prebuild" {
		profile.ExtractGithubProfile()
		return
	}

	// Eval step: run the classifier over the hand-labelled dataset and print
	// precision/recall/F1/MCC/... plus the misclassified titles. Offline quality
	// check, not part of the scheduled run:
	//   go run . eval
	if len(os.Args) > 1 && os.Args[1] == "eval" {
		evalHarness.RunEval()
		return
	}

	// Embed step: chunk profile/corpus, embed each chunk via Voyage, and write
	// profile/corpus_index.json (the Approach 3 RAG index). Run occasionally
	// (not every 4h); requires VOYAGE_API_KEY in env:
	//   go run . embed
	if len(os.Args) > 1 && os.Args[1] == "embed" {
		rag.BuildCorpusIndex()
		return
	}

	stories := GetMyHackerNewsStories()
	log.Println("stories = ", stories)

	if err := appendStoriesToMarkdown(stories); err != nil {
		log.Fatal("failed to write markdown: ", err)
	}
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
