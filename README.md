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

## CRON to run this every 4 hours

```
0 */4 * * * export ANTHROPIC_API_KEY=$(security find-generic-password -a "$USER" -s "ANTHROPIC_API_KEY" -w) && cd /Users/apple/engg/products/tech-news && /usr/local/go/bin/go run . >> /tmp/tech-news.log 2>&1
```

**Add it (one-shot):**
```bash
(crontab -l 2>/dev/null; echo '0 */4 * * * export ANTHROPIC_API_KEY=$(security find-generic-password -a "$USER" -s "ANTHROPIC_API_KEY" -w) && cd /Users/apple/engg/products/tech-news && $(which go) run . >> /tmp/tech-news.log 2>&1') | crontab -
```

## Next steps
- tests
- refactor PostJSON()
