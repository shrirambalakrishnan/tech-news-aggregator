package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/shrirambalakrishnan/tech-news/hackernews_classifier"
	"github.com/shrirambalakrishnan/tech-news/profile"
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
var loadUserContext = profile.LoadUserContext

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

// buildProfileForArm initializes the UserProfile the chosen arm requires, and
// errors (rather than failing soft) when the arm's data is missing - so the
// caller can exit non-zero instead of silently running a different arm.
//
//   - ArmGeneric   -> empty profile; the generic flow needs no user data.
//   - ArmInterests -> profile from the distilled JSON; errors if it's absent.
//   - ArmRAG       -> stubbed this iteration; always errors (corpus chunks
//     unavailable).
func buildProfileForArm(arm hackernews_classifier.Arm) (hackernews_classifier.UserProfile, error) {
	switch arm {
	case hackernews_classifier.ArmGeneric:
		return hackernews_classifier.UserProfile{}, nil
	case hackernews_classifier.ArmInterests:
		userContext, err := loadUserContext(profile.USER_CONTEXT_FILE)
		if err != nil {
			return hackernews_classifier.UserProfile{}, fmt.Errorf("arm 1 (interests): distilled user profile unavailable at %s (run `go run . prebuild`): %w", profile.USER_CONTEXT_FILE, err)
		}
		return hackernews_classifier.UserProfile{
			Summary:   userContext.Summary,
			Interests: userContext.Interests,
		}, nil
	case hackernews_classifier.ArmRAG:
		return hackernews_classifier.UserProfile{}, fmt.Errorf("arm 2 (RAG): corpus chunks unavailable - not implemented")
	default:
		return hackernews_classifier.UserProfile{}, fmt.Errorf("unknown arm: %d", arm)
	}
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

	// Build the profile the arm requires. An error here (e.g. arm 1 with no
	// distilled JSON) propagates up so the run exits non-zero rather than
	// silently classifying under a different flow.
	userProfile, err := buildProfileForArm(arm)
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
