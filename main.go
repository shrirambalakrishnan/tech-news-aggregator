package main

import (
	"fmt"
	"log"
	"os"
	"time"
)

func main() {
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
