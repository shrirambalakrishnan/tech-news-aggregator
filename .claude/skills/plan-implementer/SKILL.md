---
name: plan-implementer
description: Implement a plan produced by the plan-issue skill — branch from main, ship one verified commit per plan step, then open a PR in the house style. Takes the plan's issue-comment URL as its argument.
argument-hint: <plan-comment-url>
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, WebFetch, AskUserQuestion]
model: opus
---

# Implement a plan

The plan to implement is: **$1**

If no plan URL was given, ask for one before doing anything else.

The argument is a permalink to the plan comment, of the form
`https://github.com/<owner>/<repo>/issues/<n>#issuecomment-<comment-id>`.
Everything needed is in it — parse out the owner, repo, issue number and
comment id rather than asking for them.

```bash
gh api repos/<owner>/<repo>/issues/comments/<comment-id> -q .body   # the plan
gh issue view <n> --repo <owner>/<repo> --json number,title,body,comments
```

This runs in the main thread, not a sub-agent — the back-and-forth with the
developer is the point.

## Steps

- Read the plan at $1
- Read the issue description for more context
- Create a branch from main in format
  `feature/<issue-number>-<issue-title-briefly-as-name>`
  - `git fetch origin` first and branch from `origin/main`, so the branch
    starts from current main rather than a stale local copy
  - The working tree must be clean before starting; stop and say so if it
    is not
- For each of the steps
  - Implement each of the step as per description
  - `go build ./... && go vet ./... && gofmt -l . && go test ./...` — must
    pass before proceeding to commit
  - Add proper commit message
  - Link to this issue using `Refs`
  - Commit each step separately
  - Add yourself in the `Co-Authored-By`

  Commit message trailers, in this order:

  ```
  Refs #<issue-number>

  Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
  ```

- Only if the plan doesn't involve changes to docs — `README.md` and
  `CLAUDE.md` file — then make changes in those and make a final commit
- Never run paid work. Example — `go run . eval 2`
- Push the commits
  - First push needs `git push -u origin <branch>`
- Create a PR for this branch with description
  - Reviews happen on the PR and the PR description; hence create the PR and
    add comments to the PR without the developer's confirmation
  - The PR body uses `Refs #<n>`, **never** `Closes`/`Fixes` — the developer
    closes the issue by hand after merge
  - `What Changed` section
    - List of commits and changes made
  - `Known Limitations` section
  - `Acceptance Criteria` — status after implementation
  - `Next Steps`
  - Explicitly add "What is shipped" and "What is deferred" for future
    reference
  - Example PR description for reference:
    https://github.com/shrirambalakrishnan/tech-news-aggregator/pull/32#issue-5079049863
  - Write the PR body to a file in the scratchpad and pass it with
    `gh pr create --body-file <file>` rather than inline — bodies contain
    backticks, quotes and newlines that shell-quoting mangles
  - Report the PR URL back to the developer

## Notes

- Acceptance criteria that can only be verified by paid or long-running work
  are not verified here. Mark them as pending in the `Acceptance Criteria`
  section and list the command the developer needs to run under `Next Steps`.
- Do not merge the PR, and do not close the issue.
