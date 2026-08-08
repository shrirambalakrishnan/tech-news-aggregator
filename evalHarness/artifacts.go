package evalHarness

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/shrirambalakrishnan/tech-news/hackernews_classifier"
	"github.com/shrirambalakrishnan/tech-news/rag"
)

// This file pins the DATA a run used, as opposed to the code (git SHA) and the
// model, which the run record already carries.
//
// Both inputs that decide an eval's numbers are git-ignored and mutable in
// place: the labelled dataset, and profile/corpus_index.json, which `go run .
// embed` overwrites wholesale. Approach 5 showed what that costs - re-embedding
// over a widened corpus silently re-based every arm 2 and arm 3 number, and the
// pre-notes index only survived because someone remembered to `cp` it first.
//
// So each run hashes its inputs and archives them content-addressed: the record
// names a hash, and the bytes behind that hash are still on disk to compare
// against. Archiving is write-if-absent, so the common case (many runs over one
// dataset and one index) stores each artifact exactly once.

const (
	// DATASETS_SUBDIR holds snapshots of the labelled dataset,
	// CORPUS_INDEX_SUBDIR snapshots of the corpus index - both under
	// EVAL_RUNS_DIR, both named <sha256>.json so a record's hash IS the filename
	// of the bytes it refers to.
	DATASETS_SUBDIR     = "datasets"
	CORPUS_INDEX_SUBDIR = "corpus_index"
)

// hashFile returns the sha256 of a file's raw bytes, hex-encoded.
//
// Raw bytes, deliberately: the hash is reproducible from outside the program
// (`shasum -a 256 profile/corpus_index.json`) with no canonicalisation rules to
// agree on. The cost is that it hashes the whole file including metadata - the
// index stamps a fresh `generated_at` on every embed, so re-embedding an
// unchanged corpus yields a new hash and a second archived copy. Accepted
// (issue #26): re-embedding is a deliberate ~20-minute act, and between two eval
// runs nothing rewrites the file, so repeat runs archive nothing new.
func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read %s for hashing: %w", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// archiveIfAbsent hashes srcPath and copies it to
// EVAL_RUNS_DIR/<subdir>/<hash>.json unless that file already exists. It returns
// the hash either way, so callers get the same value whether or not a copy was
// made.
//
// Write-if-absent rather than write-always because the content addresses itself:
// a file already at <hash>.json has, by construction, the bytes we were about to
// write. Skipping the copy is what keeps a 4.4 MB index from being duplicated
// once per eval run.
func archiveIfAbsent(subdir, srcPath string) (string, error) {
	hash, err := hashFile(srcPath)
	if err != nil {
		return "", err
	}

	dir := filepath.Join(EVAL_RUNS_DIR, subdir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create %s: %w", dir, err)
	}

	dest := filepath.Join(dir, hash+".json")
	if _, err := os.Stat(dest); err == nil {
		return hash, nil // already archived by an earlier run
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("failed to check %s: %w", dest, err)
	}

	data, err := os.ReadFile(srcPath)
	if err != nil {
		return "", fmt.Errorf("failed to read %s for archiving: %w", srcPath, err)
	}
	if err := os.WriteFile(dest, data, 0644); err != nil {
		return "", fmt.Errorf("failed to archive %s to %s: %w", srcPath, dest, err)
	}

	log.Printf("eval: archived %s as %s", srcPath, dest)
	return hash, nil
}

// armUsesCorpusIndex reports whether an arm retrieves from the corpus index.
//
// It mirrors the retrieval cases of armcontext.BuildProfile's switch, and must
// be updated alongside it: an arm that reads the index but is missing here would
// record no corpus_index_hash and so publish numbers whose retrieval side is
// untraceable. Arms 0 and 1 never load the index, so hashing it for them would
// attach a provenance claim to something they did not read.
func armUsesCorpusIndex(arm hackernews_classifier.Arm) bool {
	switch arm {
	case hackernews_classifier.ArmRAG, hackernews_classifier.ArmRAGPerStory,
		hackernews_classifier.ArmRerank:
		return true
	default:
		return false
	}
}

// runArtifacts are the content hashes of a run's data inputs. CorpusIndexHash is
// empty for the non-retrieval arms, which is what keeps the field out of their
// record entirely (see RunRecord's omitempty).
type runArtifacts struct {
	DatasetHash     string
	CorpusIndexHash string
}

// archiveRunInputs snapshots everything this arm is about to read and returns
// the hashes for the record.
//
// Called BEFORE the first Claude call. Two reasons: it is free and local, so a
// failure here costs nothing, whereas failing after classification would mean an
// unrecordable run that has already been paid for; and hashing up front pins the
// exact bytes retrieval is about to read rather than whatever happens to be on
// disk once a ten-minute run finishes.
//
// Failures abort the eval rather than recording a run with an unknown input.
// Same reasoning as issue #11's no-fail-soft rule: a number nobody can trace
// back to its data is the problem this is here to solve.
func archiveRunInputs(arm hackernews_classifier.Arm, datasetPath, indexPath string) (runArtifacts, error) {
	var artifacts runArtifacts

	datasetHash, err := archiveIfAbsent(DATASETS_SUBDIR, datasetPath)
	if err != nil {
		return runArtifacts{}, fmt.Errorf("failed to archive the labelled dataset: %w", err)
	}
	artifacts.DatasetHash = datasetHash

	if !armUsesCorpusIndex(arm) {
		return artifacts, nil
	}

	indexHash, err := archiveIfAbsent(CORPUS_INDEX_SUBDIR, indexPath)
	if err != nil {
		return runArtifacts{}, fmt.Errorf("failed to archive the corpus index (run `go run . embed`): %w", err)
	}
	artifacts.CorpusIndexHash = indexHash

	return artifacts, nil
}

// ArchiveCorpusIndex snapshots the current corpus index. Exported for the embed
// step (main.runEmbed), which calls it right after writing a new index.
//
// The eval flow archives the index too, so this is not what makes an index
// traceable - it is what makes an index traceable EARLY. Without it, an index
// built today and first evaluated next week is only captured at eval time, and
// an `embed` run in between would have overwritten it with no snapshot taken.
//
// rag cannot call this itself: evalHarness already imports rag, so the reverse
// edge would be a cycle. Hence main wires it, which also keeps rag unaware that
// eval tracking exists.
func ArchiveCorpusIndex() error {
	hash, err := archiveIfAbsent(CORPUS_INDEX_SUBDIR, rag.CORPUS_INDEX_FILE)
	if err != nil {
		return fmt.Errorf("failed to archive the corpus index: %w", err)
	}
	log.Printf("embed: corpus index hash is %s", hash)
	return nil
}
