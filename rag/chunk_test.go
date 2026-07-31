package rag

import (
	"reflect"
	"testing"
)

func TestChunkTextShortTextSingleChunk(t *testing.T) {
	got := ChunkText("alpha beta gamma", 4, 1)
	want := []string{"alpha beta gamma"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestChunkTextExactWindow(t *testing.T) {
	got := ChunkText("w0 w1 w2 w3", 4, 1)
	want := []string{"w0 w1 w2 w3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestChunkTextOverlap(t *testing.T) {
	// 10 words, window 4, overlap 1 => step 3 => chunks share their edges.
	got := ChunkText("w0 w1 w2 w3 w4 w5 w6 w7 w8 w9", 4, 1)
	want := []string{
		"w0 w1 w2 w3",
		"w3 w4 w5 w6",
		"w6 w7 w8 w9",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestChunkTextEmpty(t *testing.T) {
	if got := ChunkText("   \n\t ", 4, 1); got != nil {
		t.Errorf("expected nil for whitespace-only text, got %v", got)
	}
}

func TestChunkTextCollapsesWhitespace(t *testing.T) {
	// strings.Fields normalises arbitrary whitespace runs to single spaces.
	got := ChunkText("a\n\nb\t c", 4, 1)
	want := []string{"a b c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
