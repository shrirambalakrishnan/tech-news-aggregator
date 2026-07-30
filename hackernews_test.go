package main

import (
	"errors"
	"testing"

	"github.com/shrirambalakrishnan/tech-news/hackernews_classifier"
	"github.com/shrirambalakrishnan/tech-news/profile"
	"github.com/shrirambalakrishnan/tech-news/rag"
)

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

		loadUserContext = func(path string) (profile.UserContext, error) {
			return profile.UserContext{Summary: "backend work", Interests: []string{"Go", "Spanner"}}, nil
		}
		defer func() { loadUserContext = profile.LoadUserContext }()

		classifyTechNewsStoryCallCount := 0
		classifyTechNewsStoryCallParameters := [][]hackernews_classifier.StoryDetail{}
		var classifyTechNewsStoryCallProfile hackernews_classifier.UserProfile
		classifyTechNewsStory = func(arm hackernews_classifier.Arm, stories []hackernews_classifier.StoryDetail, userProfile hackernews_classifier.UserProfile) []int {
			classifyTechNewsStoryCallCount++
			classifyTechNewsStoryCallParameters = append(classifyTechNewsStoryCallParameters, stories)
			classifyTechNewsStoryCallProfile = userProfile
			return []int{}
		}
		defer func() { classifyTechNewsStory = hackernews_classifier.ClassifyTechNewsStory }()

		if _, err := FilterHackerNewsStoriesByTitle(hackernews_classifier.ArmInterests, []HackerNewsStory{
			{StoryId: 1, Title: "story1", Author: "Author1"},
			{StoryId: 2, Title: "story2", Author: "Author1"},
			{StoryId: 3, Title: "story3", Author: "Author3"},
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if classifyTechNewsStoryCallCount != 1 {
			t.Fatalf("classifyTechNewsStory call count is invalid, expected 1, got %d", classifyTechNewsStoryCallCount)
		}

		if classifyTechNewsStoryCallProfile.Summary != "backend work" || len(classifyTechNewsStoryCallProfile.Interests) != 2 {
			t.Fatalf("expected loaded user context mapped into the profile, got %+v", classifyTechNewsStoryCallProfile)
		}

		expectedStoryDetails := []hackernews_classifier.StoryDetail{
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

		loadUserContext = func(path string) (profile.UserContext, error) {
			return profile.UserContext{Summary: "backend work"}, nil
		}
		defer func() { loadUserContext = profile.LoadUserContext }()

		classifyTechNewsStory = func(arm hackernews_classifier.Arm, stories []hackernews_classifier.StoryDetail, userProfile hackernews_classifier.UserProfile) []int {
			return []int{1, 3}
		}
		defer func() { classifyTechNewsStory = hackernews_classifier.ClassifyTechNewsStory }()

		filteredStories, err := FilterHackerNewsStoriesByTitle(hackernews_classifier.ArmInterests, []HackerNewsStory{
			{StoryId: 1, Title: "story1", Author: "Author1"},
			{StoryId: 2, Title: "story2", Author: "Author1"},
			{StoryId: 3, Title: "story3", Author: "Author3"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

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

	t.Run("ArmGeneric passes an empty profile and never loads user context", func(t *testing.T) {

		loadUserContextCalled := false
		loadUserContext = func(path string) (profile.UserContext, error) {
			loadUserContextCalled = true
			return profile.UserContext{Summary: "backend work"}, nil
		}
		defer func() { loadUserContext = profile.LoadUserContext }()

		var classifyTechNewsStoryCallProfile hackernews_classifier.UserProfile
		classifyTechNewsStory = func(arm hackernews_classifier.Arm, stories []hackernews_classifier.StoryDetail, userProfile hackernews_classifier.UserProfile) []int {
			classifyTechNewsStoryCallProfile = userProfile
			return []int{}
		}
		defer func() { classifyTechNewsStory = hackernews_classifier.ClassifyTechNewsStory }()

		if _, err := FilterHackerNewsStoriesByTitle(hackernews_classifier.ArmGeneric, []HackerNewsStory{
			{StoryId: 1, Title: "story1", Author: "Author1"},
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if loadUserContextCalled {
			t.Fatal("ArmGeneric should not load user context")
		}
		if !classifyTechNewsStoryCallProfile.IsEmpty() {
			t.Fatalf("expected an empty profile under ArmGeneric, got %+v", classifyTechNewsStoryCallProfile)
		}

	})

	t.Run("ArmInterests errors (and does not classify) when user context is unavailable", func(t *testing.T) {

		loadUserContext = func(path string) (profile.UserContext, error) {
			return profile.UserContext{}, errors.New("file not found")
		}
		defer func() { loadUserContext = profile.LoadUserContext }()

		classifyCalled := false
		classifyTechNewsStory = func(arm hackernews_classifier.Arm, stories []hackernews_classifier.StoryDetail, userProfile hackernews_classifier.UserProfile) []int {
			classifyCalled = true
			return []int{}
		}
		defer func() { classifyTechNewsStory = hackernews_classifier.ClassifyTechNewsStory }()

		_, err := FilterHackerNewsStoriesByTitle(hackernews_classifier.ArmInterests, []HackerNewsStory{
			{StoryId: 1, Title: "story1", Author: "Author1"},
		})

		if err == nil {
			t.Fatal("expected an error when arm 1 profile is unavailable, got nil")
		}
		if classifyCalled {
			t.Fatal("classifier must not run when the arm's profile is unavailable")
		}

	})

	t.Run("ArmRAG passes retrieved chunks to the classifier with the titles as query", func(t *testing.T) {

		loadUserContext = func(path string) (profile.UserContext, error) {
			t.Fatal("ArmRAG must not load the distilled user context")
			return profile.UserContext{}, nil
		}
		defer func() { loadUserContext = profile.LoadUserContext }()

		var gotQuery string
		var gotK int
		loadRagContext = func(query string, k int) ([]string, error) {
			gotQuery = query
			gotK = k
			return []string{"chunk about spanner", "chunk about raft"}, nil
		}
		defer func() { loadRagContext = rag.RetrieveContext }()

		var classifyTechNewsStoryCallProfile hackernews_classifier.UserProfile
		classifyTechNewsStory = func(arm hackernews_classifier.Arm, stories []hackernews_classifier.StoryDetail, userProfile hackernews_classifier.UserProfile) []int {
			classifyTechNewsStoryCallProfile = userProfile
			return []int{}
		}
		defer func() { classifyTechNewsStory = hackernews_classifier.ClassifyTechNewsStory }()

		if _, err := FilterHackerNewsStoriesByTitle(hackernews_classifier.ArmRAG, []HackerNewsStory{
			{StoryId: 1, Title: "story1", Author: "Author1"},
			{StoryId: 2, Title: "story2", Author: "Author2"},
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if gotQuery != "story1\nstory2" {
			t.Fatalf("expected the batch titles as retrieval query, got %q", gotQuery)
		}
		if gotK != rag.RETRIEVAL_TOP_K {
			t.Fatalf("expected k = RETRIEVAL_TOP_K (%d), got %d", rag.RETRIEVAL_TOP_K, gotK)
		}
		if len(classifyTechNewsStoryCallProfile.CorpusChunks) != 2 ||
			classifyTechNewsStoryCallProfile.CorpusChunks[0] != "chunk about spanner" {
			t.Fatalf("expected retrieved chunks on the profile, got %+v", classifyTechNewsStoryCallProfile)
		}
	})

	t.Run("ArmRAG errors (and does not classify) when retrieval fails", func(t *testing.T) {

		loadRagContext = func(query string, k int) ([]string, error) {
			return nil, errors.New("corpus index unavailable")
		}
		defer func() { loadRagContext = rag.RetrieveContext }()

		classifyCalled := false
		classifyTechNewsStory = func(arm hackernews_classifier.Arm, stories []hackernews_classifier.StoryDetail, userProfile hackernews_classifier.UserProfile) []int {
			classifyCalled = true
			return []int{}
		}
		defer func() { classifyTechNewsStory = hackernews_classifier.ClassifyTechNewsStory }()

		_, err := FilterHackerNewsStoriesByTitle(hackernews_classifier.ArmRAG, []HackerNewsStory{
			{StoryId: 1, Title: "story1", Author: "Author1"},
		})

		if err == nil {
			t.Fatal("expected an error when arm 2 retrieval fails, got nil")
		}
		if classifyCalled {
			t.Fatal("classifier must not run under arm 2 when retrieval fails - it would silently score arm 0")
		}
	})

	t.Run("ArmGeneric never retrieves rag context", func(t *testing.T) {

		loadRagContext = func(query string, k int) ([]string, error) {
			t.Fatal("ArmGeneric must not retrieve rag context")
			return nil, nil
		}
		defer func() { loadRagContext = rag.RetrieveContext }()

		classifyTechNewsStory = func(arm hackernews_classifier.Arm, stories []hackernews_classifier.StoryDetail, userProfile hackernews_classifier.UserProfile) []int {
			return []int{}
		}
		defer func() { classifyTechNewsStory = hackernews_classifier.ClassifyTechNewsStory }()

		if _, err := FilterHackerNewsStoriesByTitle(hackernews_classifier.ArmGeneric, []HackerNewsStory{
			{StoryId: 1, Title: "story1", Author: "Author1"},
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
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

		var filterHackerNewsStoriesByTitleCallArm hackernews_classifier.Arm
		filterHackerNewsStoriesByTitle = func(arm hackernews_classifier.Arm, stories []HackerNewsStory) ([]HackerNewsStory, error) {
			filterHackerNewsStoriesByTitleCallArm = arm
			filterHackerNewsStoriesByTitleCallParameters = append(filterHackerNewsStoriesByTitleCallParameters, stories)
			filterHackerNewsStoriesByTitleCalled = true
			return []HackerNewsStory{}, nil
		}
		defer func() { filterHackerNewsStoriesByTitle = FilterHackerNewsStoriesByTitle }()

		if _, err := GetMyHackerNewsStories(hackernews_classifier.ArmInterests); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if filterHackerNewsStoriesByTitleCallArm != hackernews_classifier.ArmInterests {
			t.Fatalf("expected arm %d forwarded to filter, got %d", hackernews_classifier.ArmInterests, filterHackerNewsStoriesByTitleCallArm)
		}

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
