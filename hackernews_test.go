package main

import (
	"errors"
	"testing"

	"github.com/shrirambalakrishnan/tech-news/armcontext"
	"github.com/shrirambalakrishnan/tech-news/hackernews_classifier"
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

		buildProfileForArm = func(arm hackernews_classifier.Arm, stories []hackernews_classifier.StoryDetail) (hackernews_classifier.UserProfile, error) {
			return hackernews_classifier.UserProfile{Summary: "backend work", Interests: []string{"Go", "Spanner"}}, nil
		}
		defer func() { buildProfileForArm = armcontext.BuildProfile }()

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
			t.Fatalf("expected the built profile passed to the classifier, got %+v", classifyTechNewsStoryCallProfile)
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

		classifyTechNewsStory = func(arm hackernews_classifier.Arm, stories []hackernews_classifier.StoryDetail, userProfile hackernews_classifier.UserProfile) []int {
			return []int{1, 3}
		}
		defer func() { classifyTechNewsStory = hackernews_classifier.ClassifyTechNewsStory }()

		filteredStories, err := FilterHackerNewsStoriesByTitle(hackernews_classifier.ArmGeneric, []HackerNewsStory{
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

	// The arm and the stories are the builder's whole input: the arm selects the
	// flow, and the stories are arm 2's retrieval query. Passing the mapped
	// StoryDetails (not the raw HN stories) is what lets one shared builder serve
	// both this path and the eval. Per-arm construction itself is covered in
	// armcontext/build_test.go.
	t.Run("delegates context building to buildProfileForArm with the arm and mapped stories", func(t *testing.T) {

		var gotArm hackernews_classifier.Arm
		var gotStories []hackernews_classifier.StoryDetail
		buildProfileForArm = func(arm hackernews_classifier.Arm, stories []hackernews_classifier.StoryDetail) (hackernews_classifier.UserProfile, error) {
			gotArm, gotStories = arm, stories
			return hackernews_classifier.UserProfile{RetrievedExcerpts: []string{"chunk about raft"}}, nil
		}
		defer func() { buildProfileForArm = armcontext.BuildProfile }()

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

		if gotArm != hackernews_classifier.ArmRAG {
			t.Fatalf("builder called with arm %d, want ArmRAG", gotArm)
		}
		wantStories := []hackernews_classifier.StoryDetail{{Id: 1, Title: "story1"}, {Id: 2, Title: "story2"}}
		if len(gotStories) != len(wantStories) {
			t.Fatalf("builder called with %v, want %v", gotStories, wantStories)
		}
		for i, want := range wantStories {
			if gotStories[i] != want {
				t.Fatalf("builder called with %v, want %v", gotStories, wantStories)
			}
		}
		if len(classifyTechNewsStoryCallProfile.RetrievedExcerpts) != 1 ||
			classifyTechNewsStoryCallProfile.RetrievedExcerpts[0] != "chunk about raft" {
			t.Fatalf("expected the built profile passed to the classifier, got %+v", classifyTechNewsStoryCallProfile)
		}
	})

	// A run asked for one arm must exit non-zero rather than classify under
	// another (issue #11), so a build failure has to stop before the Claude call.
	t.Run("propagates the builder error and does not classify", func(t *testing.T) {

		buildProfileForArm = func(arm hackernews_classifier.Arm, stories []hackernews_classifier.StoryDetail) (hackernews_classifier.UserProfile, error) {
			return hackernews_classifier.UserProfile{}, errors.New("distilled user profile unavailable")
		}
		defer func() { buildProfileForArm = armcontext.BuildProfile }()

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
			t.Fatal("expected an error when the arm's context cannot be built, got nil")
		}
		if classifyCalled {
			t.Fatal("classifier must not run when the arm's context is unavailable")
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
