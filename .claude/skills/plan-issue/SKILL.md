---
name: plan-issue
description: Plan a GitHub issue end to end — explore the code, confirm the issue carries enough data to plan, flag when it is doing too much, then write the implementation plan and post it as an issue comment. Takes the issue URL as its argument.
argument-hint: <issue-url>
allowed-tools: [Read, Glob, Grep, Bash, WebFetch, AskUserQuestion]
model: opus
---

# Plan an issue

The issue to plan is: **$1**

If no issue URL was given, ask for one before doing anything else.

Work through the steps below in order. This runs in the main thread, not a
sub-agent — the back-and-forth with the developer is the point.

## Steps

- Read the issue at $1
  - `gh issue view <number> --repo <owner>/<repo> --json number,title,body,state,comments`
  - Read the existing comments too, not just the body — earlier discussion on the
    ticket often carries decisions the body never got updated with.
- Explore the code
- Check if you have all data to plan
  - Ensure the issue has "Acceptance criteria" defined
  - Do not assume anything; always ask any questions you have
- Check if the issue describes too many changes
  - Check if the issue can be split into multiple issues
  - If yes, suggest the multiple tickets that can be created **before** planning
- Only start planning if the developer asks you to do so, even after you've
  proposed a split
- Create multiple steps of plan
  - Each step can be a sub-section
  - List the items to be done in each step as bullet points, or however is best
    to represent it
  - Ensure each step is implementable and will be a meaningful commit to make
- Trim the plan before posting
  - Re-read the draft against the issue's scope and cut what is not needed to
    satisfy it. Each check names something to go look at — none is a judgement
    call made from memory. **Work in this order: production code, then tests,
    then commit shape.** Cutting a production change removes its tests for free;
    trimming tests first leaves behind the code that demanded them.
    - **Can existing code already do this?** Grep for the function before
      specifying a new one, and name the existing one in the plan. Reuse is the
      first cut to look for, and the one that shrinks everything downstream.
    - **Is a new helper cheaper than the duplication it removes?** Count the
      lines: a helper called once is indirection, not reuse — inline it. If it
      is worth extracting and a similar helper already exists, state in the plan
      why that one could not be used.
    - **Does an existing test already cover this?** Grep the test files before
      specifying a new one, and cite the existing test in the plan instead. A
      duplicate test is the easiest thing to add and the hardest to notice.
    - **Are the tests proportional to the code under test?** Specify the cases
      that can actually fail, not one per branch. Test volume is the single
      largest source of plan bloat measured so far — order here is sequence, not
      weight.
    - **Is each step a meaningful commit, or is it ceremony?** One coherent
      change in one file is one commit, however many bullet points it has.
  - Re-run the split check against the DRAFT, not just the issue text. Work that
    turns out to belong to another ticket is easier to see once it is written
    down as steps. Propose it as a follow-up issue and cut it.
  - Documentation changes stay only where the change would otherwise make an
    existing document wrong. Isolate them as the last step and label them
    droppable; do not narrate the change in docs that never mentioned the thing.
- Post the plan as a new comment to the issue
  - Any plan review happens on GitHub only, so post the plan directly without
    asking for confirmation
  - `gh issue comment <number> --repo <owner>/<repo> --body-file <file>`
  - Write the body to a file in the scratchpad first rather than passing it
    inline — plans contain backticks, quotes and newlines that shell-quoting
    mangles
  - Report the resulting comment URL back to the developer

## Notes

- Planning is read-only. Do not edit files, create branches, or run anything
  that costs money or takes a long time; propose those instead.
- Posting the plan comment is the one write this skill makes.
