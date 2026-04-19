## Requirements

- find the trending HackerNews articles I will be interested to read
- send this list to me every 6 hours
	- write to a file with timestamp for me read

## Readmap
- application single llm call
	- classify whether an article is a cs tech blog or not
- application single llm call but with pre built context
	- classify whether an article will be interesting for user based on his profile
- application single llm call but augmented with RAG
	- classify whether an article will be interesting for user based on his detailed profile

## Tech Stack
- Go script
- Claude / Open AI API for LLM Calls

## CRON to run this every 4 hours
0 */4 * * * cd /Users/apple/engg/products/tech-news && /usr/local/go/bin/go run . >> /tmp/tech-news.log 2>&1
