package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/shrirambalakrishnan/tech-news/hackernews_classifier"
	"github.com/shrirambalakrishnan/tech-news/profile"
	"github.com/shrirambalakrishnan/tech-news/rag"
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
var loadRagContext = rag.RetrieveContext

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

func FilterHackerNewsStoriesByTitle(stories []HackerNewsStory) []HackerNewsStory {
	storiesWithTitle := []hackernews_classifier.StoryDetail{}
	for _, story := range stories {
		storiesWithTitle = append(storiesWithTitle, hackernews_classifier.StoryDetail{
			Id:    story.StoryId,
			Title: story.Title,
		})
	}
	filteredStories := []HackerNewsStory{}

	// Load the prebuilt interest profile and fail soft to the static rules if the
	// artifact is missing (prebuild may not have run).
	var userProfile hackernews_classifier.UserProfile
	if userContext, err := loadUserContext(profile.USER_CONTEXT_FILE); err != nil {
		log.Println("user context unavailable, using static classification rules:", err)
	} else {
		userProfile = hackernews_classifier.UserProfile{
			Summary:   userContext.Summary,
			Interests: userContext.Interests,
		}
	}

	// Approach 3 (RAG): retrieve the corpus chunks most similar to this batch
	// and hand them to the classifier, which prefers them over Summary/Interests.
	// The batch's titles double as the retrieval query — the known
	// confirmation-bias trade-off (see CLAUDE.md, retrieval-key problem). Fail
	// soft: if the index is missing (embed step not run) or retrieval fails, the
	// profile loaded above / static rules still apply.
	titles := make([]string, 0, len(storiesWithTitle))
	for _, story := range storiesWithTitle {
		titles = append(titles, story.Title)
	}
	if chunks, err := loadRagContext(strings.Join(titles, "\n"), rag.RETRIEVAL_TOP_K); err != nil {
		log.Println("rag context unavailable, using profile/static rules:", err)
	} else {
		userProfile.CorpusChunks = chunks
	}

	filteredStoryIds := classifyTechNewsStory(storiesWithTitle, userProfile)
	log.Println("filteredStoryIds = ", filteredStoryIds)

	for _, story := range stories {
		for _, id := range filteredStoryIds {
			if story.StoryId == id {
				filteredStories = append(filteredStories, story)
			}
		}
	}

	return filteredStories
}

func GetMyHackerNewsStories() []HackerNewsStory {
	log.Println("GetMyHackerNewsStories...")

	stories := getHackerNewsStories()
	log.Println("stories = ", stories)

	filteredStories := filterHackerNewsStoriesByTitle(stories)
	log.Println("filteredStories = ", filteredStories)

	return filteredStories
}
