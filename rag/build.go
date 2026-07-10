package rag

import (
	"fmt"
	"log"
	"time"

	"github.com/shrirambalakrishnan/tech-news/voyageapi"
)

// This file owns the PIPELINE: it wires the stages together
// (corpus.go read -> chunk.go transform -> embed -> index.go artifact)
// and holds no stage logic of its own.

// embedDocuments is the embedding seam (function-variable DI): tests swap it for
// a fake so buildIndex runs without network calls.
var embedDocuments = voyageapi.EmbedDocuments

// buildIndex chunks every file, embeds all chunks in one (batched) call, and
// assembles the CorpusIndex. It performs no disk I/O so it is unit-testable with
// a fake embedDocuments.
func buildIndex(files []corpusFile) (CorpusIndex, error) {
	var chunks []Chunk
	var texts []string
	for _, f := range files {
		for i, piece := range ChunkText(f.Content, CHUNK_WINDOW_WORDS, CHUNK_OVERLAP_WORDS) {
			chunks = append(chunks, Chunk{
				Source:     f.Name,
				Type:       f.Type,
				ChunkIndex: i,
				Text:       piece,
			})
			texts = append(texts, piece)
		}
	}

	if len(chunks) == 0 {
		return CorpusIndex{}, fmt.Errorf("no chunks produced from %d files", len(files))
	}

	embeddings, err := embedDocuments(texts)
	if err != nil {
		return CorpusIndex{}, fmt.Errorf("failed to embed chunks: %w", err)
	}
	if len(embeddings) != len(chunks) {
		return CorpusIndex{}, fmt.Errorf("embedding count %d does not match chunk count %d", len(embeddings), len(chunks))
	}

	for i := range chunks {
		chunks[i].Embedding = embeddings[i]
	}

	return CorpusIndex{
		SchemaVersion: CORPUS_INDEX_SCHEMA_VERSION,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Model:         voyageapi.VOYAGE_EMBEDDING_MODEL,
		Dimension:     len(embeddings[0]),
		ChunkCount:    len(chunks),
		Chunks:        chunks,
	}, nil
}

// BuildCorpusIndex is the embed entrypoint (go run . embed): it reads the corpus,
// builds the embedding index, and writes it to disk. It fails soft with a log
// line (like the prebuild step) rather than panicking.
func BuildCorpusIndex() {
	log.Println("===== Building corpus embedding index =====")

	files, err := readCorpusFiles(CORPUS_DIR)
	if err != nil {
		log.Printf("failed to read corpus files: %v", err)
		return
	}
	if len(files) == 0 {
		log.Printf("no corpus files found in %s; nothing to embed", CORPUS_DIR)
		return
	}
	log.Printf("read %d corpus files from %s", len(files), CORPUS_DIR)

	index, err := buildIndex(files)
	if err != nil {
		log.Printf("failed to build corpus index: %v", err)
		return
	}

	if err := WriteCorpusIndex(index, CORPUS_INDEX_FILE); err != nil {
		log.Printf("failed to write corpus index: %v", err)
		return
	}

	log.Printf("Wrote corpus index to %s (%d chunks from %d files, dimension %d, model %s)",
		CORPUS_INDEX_FILE, index.ChunkCount, len(files), index.Dimension, index.Model)
}
