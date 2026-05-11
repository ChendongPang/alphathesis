package rag

import (
	"context"
	"errors"
	"strings"
	"testing"

	"alphathesis/client"
)

// mockEmbedder assigns a fixed embedding per text via a lookup table.
// Texts not in the table receive a zero vector.
type mockEmbedder struct {
	table     map[string][]float64
	callCount int
	failAfter int // if > 0, fail on the Nth call
}

func (m *mockEmbedder) CreateEmbedding(_ context.Context, req client.EmbeddingRequest) (*client.EmbeddingResponse, error) {
	m.callCount++
	if m.failAfter > 0 && m.callCount >= m.failAfter {
		return nil, errors.New("mock embed error")
	}
	inputs, _ := req.Input.([]string)
	resp := &client.EmbeddingResponse{}
	for i, text := range inputs {
		emb := m.table[text]
		if emb == nil {
			emb = make([]float64, 4)
		}
		resp.Data = append(resp.Data, client.EmbeddingData{
			Index:     i,
			Embedding: emb,
		})
	}
	return resp, nil
}

// unit vectors for controlled cosine similarity
var (
	vecA = []float64{1, 0, 0, 0} // points in dimension 0
	vecB = []float64{0, 1, 0, 0} // points in dimension 1
	vecC = []float64{0, 0, 1, 0} // points in dimension 2
)

const (
	chunkA = "iPhone revenue grew strongly this quarter."
	chunkB = "Services segment reached an all-time high margin."
	chunkC = "Mac shipments declined due to weak consumer demand."
)

func newMockEngine(table map[string][]float64) *RAGEngine {
	return New(&mockEmbedder{table: table}, "test-embed-model",
		WithChunkSize(200),
		WithBatchSize(10),
	)
}

func newMockEngineWithMinSimilarity(table map[string][]float64, minSim float64) *RAGEngine {
	return New(&mockEmbedder{table: table}, "test-embed-model",
		WithChunkSize(200),
		WithBatchSize(10),
		WithMinSimilarity(minSim),
	)
}

func TestRecallTopK(t *testing.T) {
	// fullText has three paragraphs → three chunks
	fullText := chunkA + "\n\n" + chunkB + "\n\n" + chunkC
	table := map[string][]float64{
		chunkA: vecA,
		chunkB: vecB,
		chunkC: vecC,
	}
	engine := newMockEngineWithMinSimilarity(table, 0)

	// Assumption embedding points toward vecB → should recall chunkB first
	assumptions := []AssumptionInput{
		{ID: 1, Embedding: vecB},
	}
	result, err := engine.Recall(context.Background(), fullText, assumptions, 1)
	if err != nil {
		t.Fatal(err)
	}
	chunks, ok := result[1]
	if !ok || len(chunks) != 1 {
		t.Fatalf("result[1] = %v", result[1])
	}
	if chunks[0] != chunkB {
		t.Errorf("top chunk = %q, want %q", chunks[0], chunkB)
	}
}

func TestRecallMultipleAssumptions(t *testing.T) {
	fullText := chunkA + "\n\n" + chunkB + "\n\n" + chunkC
	table := map[string][]float64{
		chunkA: vecA,
		chunkB: vecB,
		chunkC: vecC,
	}
	engine := newMockEngineWithMinSimilarity(table, 0)

	assumptions := []AssumptionInput{
		{ID: 10, Embedding: vecA},
		{ID: 20, Embedding: vecC},
	}
	result, err := engine.Recall(context.Background(), fullText, assumptions, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result[10][0] != chunkA {
		t.Errorf("assumption 10 top = %q, want %q", result[10][0], chunkA)
	}
	if result[20][0] != chunkC {
		t.Errorf("assumption 20 top = %q, want %q", result[20][0], chunkC)
	}
}

func TestRecallTopKGreaterThanChunks(t *testing.T) {
	fullText := chunkA + "\n\n" + chunkB
	table := map[string][]float64{
		chunkA: vecA,
		chunkB: vecB,
	}
	engine := newMockEngineWithMinSimilarity(table, 0)

	result, err := engine.Recall(context.Background(), fullText, []AssumptionInput{{ID: 1, Embedding: vecA}}, 10)
	if err != nil {
		t.Fatal(err)
	}
	// Should return all available chunks, not panic
	if len(result[1]) != 2 {
		t.Errorf("len(result[1]) = %d, want 2", len(result[1]))
	}
}

func TestRecallEmptyText(t *testing.T) {
	engine := newMockEngine(nil)
	result, err := engine.Recall(context.Background(), "", []AssumptionInput{{ID: 1, Embedding: vecA}}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty result, got %v", result)
	}
}

func TestRecallNoAssumptions(t *testing.T) {
	engine := newMockEngine(nil)
	result, err := engine.Recall(context.Background(), "some text here for testing purposes", nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty result, got %v", result)
	}
}

func TestRecallSkipsZeroEmbedding(t *testing.T) {
	fullText := chunkA + "\n\n" + chunkB
	table := map[string][]float64{chunkA: vecA, chunkB: vecB}
	engine := newMockEngine(table)

	assumptions := []AssumptionInput{
		{ID: 1, Embedding: nil},   // zero → skipped
		{ID: 2, Embedding: vecA},  // valid
	}
	result, err := engine.Recall(context.Background(), fullText, assumptions, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result[1]; ok {
		t.Error("assumption with nil embedding should be skipped")
	}
	if result[2][0] != chunkA {
		t.Errorf("result[2][0] = %q, want %q", result[2][0], chunkA)
	}
}

func TestRecallBatching(t *testing.T) {
	// Build 5 chunks and set batchSize=2 to force multiple embed calls.
	paragraphs := make([]string, 5)
	table := make(map[string][]float64)
	for i := range paragraphs {
		// Each paragraph is long enough to survive the minChunkChars filter
		paragraphs[i] = strings.Repeat("word ", 8) // 40 chars
		emb := make([]float64, 4)
		emb[i%4] = 1
		table[strings.TrimSpace(paragraphs[i])] = emb
	}
	fullText := strings.Join(paragraphs, "\n\n")

	mock := &mockEmbedder{table: table}
	engine := New(mock, "model", WithChunkSize(200), WithBatchSize(2))

	_, err := engine.Recall(context.Background(), fullText, []AssumptionInput{{ID: 1, Embedding: vecA}}, 3)
	if err != nil {
		t.Fatal(err)
	}
	// 5 chunks / batchSize 2 = 3 calls
	if mock.callCount != 3 {
		t.Errorf("callCount = %d, want 3", mock.callCount)
	}
}

func TestRecallEmbedError(t *testing.T) {
	fullText := chunkA + "\n\n" + chunkB
	mock := &mockEmbedder{table: map[string][]float64{}, failAfter: 1}
	engine := New(mock, "model", WithChunkSize(200), WithBatchSize(10))

	_, err := engine.Recall(context.Background(), fullText, []AssumptionInput{{ID: 1, Embedding: vecA}}, 1)
	if err == nil {
		t.Fatal("expected error from embed failure")
	}
}

func TestTopKByCosine(t *testing.T) {
	query := []float64{1, 0, 0, 0}
	candidates := [][]float64{
		{0, 1, 0, 0}, // sim=0
		{1, 0, 0, 0}, // sim=1  ← best
		{0, 0, 1, 0}, // sim=0
	}
	idxs := topKByCosine(query, candidates, 1, 0)
	if len(idxs) != 1 || idxs[0] != 1 {
		t.Errorf("topKByCosine = %v, want [1]", idxs)
	}
}

func TestTopKByCosineZeroQuery(t *testing.T) {
	idxs := topKByCosine([]float64{0, 0, 0}, [][]float64{{1, 0, 0}}, 1, 0)
	if len(idxs) != 0 {
		t.Errorf("expected empty result for zero query, got %v", idxs)
	}
}
