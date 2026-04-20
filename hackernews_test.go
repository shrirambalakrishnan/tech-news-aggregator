package main

import "testing"

func TestGetHackerNewsStories(t *testing.T) {

	t.Run("test call count to GetHackerNewsStoriesInPage()", func(t *testing.T) {
		HACKERNEWS_NUM_PAGES_TO_QUERY = 2

		callCount := 0
		getHackerNewsStoriesInPage = func(page int) []HackerNewsStory {
			callCount++
			return []HackerNewsStory{}
		}
		defer func() { getHackerNewsStoriesInPage = GetHackerNewsStoriesInPage }()

		GetHackerNewsStories()

		if callCount != HACKERNEWS_NUM_PAGES_TO_QUERY {
			t.Fatalf("expected %d calls to GetHackerNewsStoriesInPage, got %d", HACKERNEWS_NUM_PAGES_TO_QUERY, callCount)
		}
	})

	t.Run("test stories returned by GetHackerNewsStories()", func(t *testing.T) {

		HACKERNEWS_NUM_PAGES_TO_QUERY = 2

		getHackerNewsStoriesInPage = func(page int) []HackerNewsStory {
			if page == 0 {
				return []HackerNewsStory{
					{Title: "story1"},
					{Title: "story2"},
				}
			} else if page == 1 {
				return []HackerNewsStory{
					{Title: "story3"},
				}
			} else if page == 2 {
				return []HackerNewsStory{
					{Title: "story4"},
				}
			}

			return []HackerNewsStory{}
		}
		defer func() { getHackerNewsStoriesInPage = GetHackerNewsStoriesInPage }()

		stories := GetHackerNewsStories()

		if len(stories) != 3 {
			t.Fatalf("expected 3 stories, got %d", len(stories))
		}

	})

	t.Run("test parameters for GetHackerNewsStoriesInPage()", func(t *testing.T) {
		getHackerNewsStoriesInPageCalledWithParams := []int{}

		HACKERNEWS_NUM_PAGES_TO_QUERY = 2

		getHackerNewsStoriesInPage = func(page int) []HackerNewsStory {
			getHackerNewsStoriesInPageCalledWithParams = append(getHackerNewsStoriesInPageCalledWithParams, page)
			if page == 0 {
				return []HackerNewsStory{
					{Title: "story1"},
					{Title: "story2"},
				}
			} else if page == 1 {
				return []HackerNewsStory{
					{Title: "story3"},
				}
			} else if page == 2 {
				return []HackerNewsStory{
					{Title: "story4"},
				}
			}

			return []HackerNewsStory{}
		}
		defer func() { getHackerNewsStoriesInPage = GetHackerNewsStoriesInPage }()

		GetHackerNewsStories()

		for i := 0; i < len(getHackerNewsStoriesInPageCalledWithParams); i++ {
			if getHackerNewsStoriesInPageCalledWithParams[i] >= HACKERNEWS_NUM_PAGES_TO_QUERY {
				t.Fatalf("expected page should be less than %d, got %d", HACKERNEWS_NUM_PAGES_TO_QUERY, getHackerNewsStoriesInPageCalledWithParams[i])
			}
		}

	})

}

func TestFilterHackerNewsStoriesByTitle(t *testing.T) {

	t.Run("calls ClassifyTechNewsStory with correct parameters", func(t *testing.T) {

		classifyTechNewsStoryCallCount := 0
		classifyTechNewsStoryCallParameters := [][]StoryDetail{}
		classifyTechNewsStory = func(stories []StoryDetail) []int {
			classifyTechNewsStoryCallCount++
			classifyTechNewsStoryCallParameters = append(classifyTechNewsStoryCallParameters, stories)
			return []int{}
		}
		defer func() { classifyTechNewsStory = ClassifyTechNewsStory }()

		FilterHackerNewsStoriesByTitle([]HackerNewsStory{
			{StoryId: 1, Title: "story1", Author: "Author1"},
			{StoryId: 2, Title: "story2", Author: "Author1"},
			{StoryId: 3, Title: "story3", Author: "Author3"},
		})

		if classifyTechNewsStoryCallCount != 1 {
			t.Fatalf("classifyTechNewsStory call count is invalid, expected 1, got %d", classifyTechNewsStoryCallCount)
		}

		expectedStoryDetails := []StoryDetail{
			{Id: 1, Title: "story1"},
			{Id: 2, Title: "story2"},
			{Id: 3, Title: "story3"},
		}

		if len(classifyTechNewsStoryCallParameters[0]) != len(expectedStoryDetails) {
			t.Fatalf("classifyTechNewsStory call parameters are invalid, expected %v, got %v", expectedStoryDetails, classifyTechNewsStoryCallParameters[0])
		}

		for i, story := range classifyTechNewsStoryCallParameters[0] {
			if story != expectedStoryDetails[i] {
				t.Fatalf("classifyTechNewsStory call parameters are invalid, expected %v, got %v", expectedStoryDetails, classifyTechNewsStoryCallParameters[0])
			}
		}

	})

	t.Run("returns filtered stories based on ClassifyTechNewsStory response", func(t *testing.T) {

		classifyTechNewsStory = func(stories []StoryDetail) []int {
			return []int{1, 3}
		}
		defer func() { classifyTechNewsStory = ClassifyTechNewsStory }()

		filteredStories := FilterHackerNewsStoriesByTitle([]HackerNewsStory{
			{StoryId: 1, Title: "story1", Author: "Author1"},
			{StoryId: 2, Title: "story2", Author: "Author1"},
			{StoryId: 3, Title: "story3", Author: "Author3"},
		})

		expectedFilteredStories := []HackerNewsStory{
			{StoryId: 1, Title: "story1", Author: "Author1"},
			{StoryId: 3, Title: "story3", Author: "Author3"},
		}

		if len(filteredStories) != len(expectedFilteredStories) {
			t.Fatalf("filtered stories length is invalid, expected %d, got %d", len(expectedFilteredStories), len(filteredStories))
		}

		for i, story := range filteredStories {
			if story != expectedFilteredStories[i] {
				t.Fatalf("filtered stories are invalid, expected %v, got %v", expectedFilteredStories, filteredStories)
			}
		}

	})

}

func TestGetMyHackerNewsStories(t *testing.T) {

	t.Run("calls required functions to fetch relevant stories", func(t *testing.T) {
		getHackerNewsStoriesCalled := false
		filterHackerNewsStoriesByTitleCalled := false
		filterHackerNewsStoriesByTitleCallParameters := [][]HackerNewsStory{}

		getHackerNewsStories = func() []HackerNewsStory {
			getHackerNewsStoriesCalled = true
			return []HackerNewsStory{
				{StoryId: 1, Title: "story1", Author: "Author1"},
				{StoryId: 2, Title: "story2", Author: "Author1"},
			}
		}
		defer func() { getHackerNewsStories = GetHackerNewsStories }()

		filterHackerNewsStoriesByTitle = func(stories []HackerNewsStory) []HackerNewsStory {
			filterHackerNewsStoriesByTitleCallParameters = append(filterHackerNewsStoriesByTitleCallParameters, stories)
			filterHackerNewsStoriesByTitleCalled = true
			return []HackerNewsStory{}
		}
		defer func() { filterHackerNewsStoriesByTitle = FilterHackerNewsStoriesByTitle }()

		GetMyHackerNewsStories()

		if !getHackerNewsStoriesCalled {
			t.Fatal("GetHackerNewsStories was not called")
		}

		expectedParamsToFilterHackerNewsStoriesByTitle := []HackerNewsStory{
			{StoryId: 1, Title: "story1", Author: "Author1"},
			{StoryId: 2, Title: "story2", Author: "Author1"},
		}

		if len(filterHackerNewsStoriesByTitleCallParameters) != 1 {
			t.Fatalf("expected FilterHackerNewsStoriesByTitle to be called once, but was called %d times", len(filterHackerNewsStoriesByTitleCallParameters))
		}

		for i, story := range filterHackerNewsStoriesByTitleCallParameters[0] {
			if story != expectedParamsToFilterHackerNewsStoriesByTitle[i] {
				t.Fatalf("FilterHackerNewsStoriesByTitle was called with invalid parameters, expected %v, got %v", expectedParamsToFilterHackerNewsStoriesByTitle, filterHackerNewsStoriesByTitleCallParameters[0])
			}
		}

		if !filterHackerNewsStoriesByTitleCalled {
			t.Fatal("FilterHackerNewsStoriesByTitle was not called")
		}
	})

}
