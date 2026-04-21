## Problem Statement

Current classification prompt depends entirely on specific points listed in the system prompt.
We want to make this classification rule dynamic.

---

## Plan

### Step 1: GitHub Integration
- Read GitHub username from a config file
- Call `GET /users/{username}/repos?sort=updated&direction=desc&per_page=20` to get the 20 most recently updated repos — no pagination needed
- For each repo, collect: name, description, topics, and README
- Fetch README as raw text using: `https://raw.githubusercontent.com/{owner}/{repo}/{default_branch}/README.md`
  - Use the `default_branch` field from the repo response to construct the URL (branch may be `main` or `master` or other)
  - Skip silently if README is not found (404)

### Step 2: Extract User Interests
- Send all READMEs in a single LLM call (well within Claude's 200K token context window)
- Prompt the LLM to extract 10-30 specific technical interest keywords (scaled to how diverse the repos are)
- Keywords should be specific enough to map to HN story titles (e.g. "WebAssembly runtimes" not "programming")
- Output format: flat JSON array of strings — `["Rust", "compilers", "distributed systems", ...]`
- Store the result in `profile/user_context.json` with a `generated_at` timestamp:
  ```json
  {
    "generated_at": "2026-04-21T10:00:00Z",
    "interests": ["Rust", "compilers", "WebAssembly", "distributed systems"]
  }
  ```
- This step runs once as a `prebuild` command — the main script reads from this file on each run

### Step 3: Update Classification System Prompt
- Replace the hardcoded classification rules in `ConstructPromptSystemAttribute`
- Inject the extracted interest keywords into the system prompt
- Stories are then classified based on the user's actual interests rather than a fixed ruleset

---

## Notes
- README-only approach is sufficient — READMEs are human-curated summaries of what a repo is about
- Even sparse repos (name + description + topics) provide enough signal for interest extraction
- No filtering of repos (e.g. forks) for now — can be revisited later
- More profile sources (Substack, Medium) can be added in future iterations following the same pattern
