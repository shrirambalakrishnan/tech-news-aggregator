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
