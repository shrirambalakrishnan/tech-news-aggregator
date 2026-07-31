package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/shrirambalakrishnan/tech-news/armcontext"
	"github.com/shrirambalakrishnan/tech-news/hackernews_classifier"
)

const (
	HACKERNEWS_API_HOST              = "http://hn.algolia.com/api/"
	HACKERNEWS_API_VERSION           = "v1"
	HACKERNEWS_STORIES_LIST_ENDPOINT = "search?"
	HACKERNEWS_STORIES_LIST_URL      = HACKERNEWS_API_HOST + HACKERNEWS_API_VERSION + "/" + HACKERNEWS_STORIES_LIST_ENDPOINT
)

var HACKERNEWS_HITS_PER_PAGE = 30
var HACKERNEWS_NUM_PAGES_TO_QUERY = 1

var getHackerNewsStoriesInPage = GetHackerNewsStoriesInPage
var filterHackerNewsStoriesByTitle = FilterHackerNewsStoriesByTitle
var classifyTechNewsStory = hackernews_classifier.ClassifyTechNewsStory
var getHackerNewsStories = GetHackerNewsStories
var buildProfileForArm = armcontext.BuildProfile

type HackerNewsStory struct {
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
	ObjectID  string `json:"objectID"`
	StoryId   int    `json:"story_id"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updated_at"`
}

type HackerNewsStoriesResponse struct {
	Hits        []HackerNewsStory `json:"hits"`
	HitsPerPage int               `json:"hitsPerPage"`
	NbHits      int               `json:"nbHits"`
	Page        int               `json:"page"`
	NbPages     int               `json:"nbPages"`
}

func GetHackerNewsStories() []HackerNewsStory {
	log.Println("GetHackerNewsStories...")

	stories := []HackerNewsStory{}

	for page := 0; page < HACKERNEWS_NUM_PAGES_TO_QUERY; page++ {
		log.Println("GetHackerNewsStories fetching page = ", page)

		pageStories := getHackerNewsStoriesInPage(page)
		stories = append(stories, pageStories...)
	}

	log.Println("GetHackerNewsStories stories = ", stories)

	return stories
}

func GetHackerNewsStoriesInPage(page int) []HackerNewsStory {
	log.Println("GetHackerNewsStoriesInPage page = ", page)

	queryParams := "tags=front_page&page=" + strconv.Itoa(page) + "&hitsPerPage=" + strconv.Itoa(HACKERNEWS_HITS_PER_PAGE)
	storiesUrl := HACKERNEWS_STORIES_LIST_URL + queryParams

	log.Println("storiesUrl = ", storiesUrl)

	res, err := http.Get(storiesUrl)
	if err != nil {
		panic(err)
	}
	defer res.Body.Close()

	var storiesResponse HackerNewsStoriesResponse
	if err := json.NewDecoder(res.Body).Decode(&storiesResponse); err != nil {
		panic(err)
	}

	log.Println("GetHackerNewsStoriesInPage page, storiesResponse = ", page, storiesResponse)
	return storiesResponse.Hits
}

func FilterHackerNewsStoriesByTitle(arm hackernews_classifier.Arm, stories []HackerNewsStory) ([]HackerNewsStory, error) {
	storiesWithTitle := []hackernews_classifier.StoryDetail{}
	for _, story := range stories {
		storiesWithTitle = append(storiesWithTitle, hackernews_classifier.StoryDetail{
			Id:    story.StoryId,
			Title: story.Title,
		})
	}
	filteredStories := []HackerNewsStory{}

	// Build the context this arm classifies against - including, under arm 2,
	// the excerpts retrieved for these very stories. Any failure (arm 1 without
	// its distilled JSON, arm 2 without its index) propagates up so the run
	// exits non-zero instead of silently classifying under a different flow.
	userProfile, err := buildProfileForArm(arm, storiesWithTitle)
	if err != nil {
		return nil, err
	}

	filteredStoryIds := classifyTechNewsStory(arm, storiesWithTitle, userProfile)
	log.Println("filteredStoryIds = ", filteredStoryIds)

	for _, story := range stories {
		for _, id := range filteredStoryIds {
			if story.StoryId == id {
				filteredStories = append(filteredStories, story)
			}
		}
	}

	return filteredStories, nil
}

func GetMyHackerNewsStories(arm hackernews_classifier.Arm) ([]HackerNewsStory, error) {
	log.Println("GetMyHackerNewsStories...")

	stories := getHackerNewsStories()
	log.Println("stories = ", stories)

	filteredStories, err := filterHackerNewsStoriesByTitle(arm, stories)
	if err != nil {
		return nil, err
	}
	log.Println("filteredStories = ", filteredStories)

	return filteredStories, nil
}
