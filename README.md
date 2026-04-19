## Requirements

- find the trending HackerNews articles I will be interested to read
- send this list to me every 6 hours
	- write to a file with timestamp for me read

## Roadmap
- application single llm call
	- classify whether an article is a cs tech blog or not
- application single llm call but with pre built context
	- classify whether an article will be interesting for user based on his profile
- application single llm call but augmented with RAG
	- classify whether an article will be interesting for user based on his detailed profile

## Tech Stack
- Go script
- Claude / Open AI API for LLM Calls

## Setup

### API Key (macOS Keychain)

Cron jobs don't inherit shell environment variables, so the API key must be stored in macOS Keychain.

**Store the key (one-time):**
```bash
security add-generic-password -a "$USER" -s "ANTHROPIC_API_KEY" -w "your-api-key-here"
```

**Verify it works:**
```bash
security find-generic-password -a "$USER" -s "ANTHROPIC_API_KEY" -w
```

## Runner script

`tech-news-run.sh` fetches the key from keychain and runs the Go program. Make it executable:

```bash
chmod +x tech-news-run.sh
```

## Scheduling with launchd (every 4 hours)

Cron on macOS can't access the login keychain, so we use `launchd` instead.

**Install:**
```bash
sed "s|REPO_PATH|$(pwd)|g" com.technews.plist > ~/Library/LaunchAgents/com.technews.plist
launchctl load ~/Library/LaunchAgents/com.technews.plist
```

**Verify it's loaded:**
```bash
launchctl list | grep technews
```

**Check logs:**
```bash
tail -f /tmp/tech-news.log
```

**Uninstall:**
```bash
launchctl unload ~/Library/LaunchAgents/com.technews.plist
rm ~/Library/LaunchAgents/com.technews.plist
```


## Next steps
- tests
- refactor PostJSON()
