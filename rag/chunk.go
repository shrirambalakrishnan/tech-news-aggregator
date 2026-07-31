package rag

import "strings"

const (
	CHUNK_WINDOW_WORDS  = 800
	CHUNK_OVERLAP_WORDS = 120
)

// ChunkText splits text into overlapping windows of `window` words. Consecutive
// chunks advance by step = window - overlap, so each chunk repeats the last
// `overlap` words of the previous one. The overlap prevents an idea that
// straddles a window boundary from being split across two chunks where neither
// captures it whole. Text shorter than one window yields a single chunk; empty
// or whitespace-only text yields no chunks.
//
// It is pure (no I/O) so the window/overlap math is unit-tested directly. window
// and overlap are parameters — not hardcoded — because they are the main
// retrieval-quality knobs (production passes CHUNK_WINDOW_WORDS /
// CHUNK_OVERLAP_WORDS).
func ChunkText(text string, window, overlap int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	if window <= 0 {
		return []string{strings.Join(words, " ")}
	}

	step := window - overlap
	if step <= 0 {
		// Guard against overlap >= window, which would never advance.
		step = window
	}

	// Slide the window across the words, `step` words at a time. The chunk whose
	// window reaches the last word is the final one: sliding further would only
	// produce a leftover chunk already contained in this one's overlap region.
	var chunks []string
	for start := 0; ; start += step {
		end := min(start+window, len(words))
		chunks = append(chunks, strings.Join(words[start:end], " "))
		if end == len(words) {
			return chunks
		}
	}
}
