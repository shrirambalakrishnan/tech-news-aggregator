package profile

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

type GithubRepository struct {
	Id            int      `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Topics        []string `json:"topics"`
	DefaultBranch string   `json:"default_branch"`
}

const (
	GITHUB_API_DOMAIN            = "https://api.github.com"
	GITHUB_RAW_CONTENT_DOMAIN    = "https://raw.githubusercontent.com"
	GITHUB_REPOSITORIES_ENDPOINT = "/users/%s/repos"
	GITHUB_REPOSITORIES_LIMIT    = 2
	GITHUB_README_RAW_ENDPOINT   = "/%s/%s/%s/README.md"
)

func ExtractGithubProfile() {
	log.Println("===== Extracting Github Profile =====")

	githubUsername := os.Getenv("GITHUB_USERNAME")
	if githubUsername == "" {
		log.Println("GITHUB_USERNAME environment variable is not set. Skipping Github profile extraction.")
		return
	}
	log.Println("found githubUsername = ", githubUsername)

	repoReadmes, err := FetchRepoReadmes(githubUsername)
	if err != nil {
		log.Printf("failed to fetch repository READMEs for user %s: %v", githubUsername, err)
		return
	}

	log.Printf("Successfully fetched %d repository READMEs for user %s - %s", len(repoReadmes), githubUsername, repoReadmes)

}
func FetchRepoReadmes(githubUsername string) ([]string, error) {
	repositories, err := FetchRepositories(githubUsername)
	if err != nil {
		log.Printf("failed to fetch repositories for user %s: %v", githubUsername, err)
		return nil, err
	}

	repoReadmes := []string{}

	for _, repo := range repositories {
		log.Printf("Repository: %s - %s", repo.Name, repo.Description)
		readmeContent, err := FetchReadme(repo)
		if err != nil {
			log.Printf("failed to fetch README for repository %s: %v", repo.Name, err)
			continue
		}
		repoReadmes = append(repoReadmes, readmeContent)
	}

	log.Println("===== Github Profile extraction complete =====")

	return repoReadmes, nil
}

func FetchRepositories(username string) ([]GithubRepository, error) {
	log.Println("FetchRepositories - username = ", username)

	var GITHUB_REPOSITORIES_URL = GITHUB_API_DOMAIN + fmt.Sprintf(GITHUB_REPOSITORIES_ENDPOINT, os.Getenv("GITHUB_USERNAME")) + "?per_page=" + fmt.Sprint(GITHUB_REPOSITORIES_LIMIT) + "&sort=updated&direction=desc"
	log.Println("GITHUB_REPOSITORIES_URL = ", GITHUB_REPOSITORIES_URL)

	resp, err := http.Get(GITHUB_REPOSITORIES_URL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch repositories: %s", resp.Status)
	}

	var repositories []GithubRepository
	err = json.NewDecoder(resp.Body).Decode(&repositories)
	if err != nil {
		return nil, fmt.Errorf("failed to decode repositories: %w", err)
	}
	return repositories, nil
}

func FetchReadme(repository GithubRepository) (string, error) {

	repoReadmeUrl := GITHUB_RAW_CONTENT_DOMAIN + fmt.Sprintf(GITHUB_README_RAW_ENDPOINT, os.Getenv("GITHUB_USERNAME"), repository.Name, repository.DefaultBranch)
	log.Println("repoReadmeUrl = ", repoReadmeUrl)
	resp, err := http.Get(repoReadmeUrl)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	log.Println("resp.StatusCode = ", resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to fetch README: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read README content: %w", err)
	}

	return string(body), nil
}
