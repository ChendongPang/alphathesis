package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/pgvector/pgvector-go"
	"alphathesis/client"
	"alphathesis/store"
)

// mockEmbedder implements rag.Embedder with a fixed lookup table.
type mockEmbedder struct {
	table     map[string][]float64
	callCount int
	failAfter int
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
		resp.Data = append(resp.Data, client.EmbeddingData{Index: i, Embedding: emb})
	}
	return resp, nil
}

// mockStore records calls to UpdateAssumptionEmbedding.
type mockStore struct {
	stored map[int64]pgvector.Vector
	failID int64
}

func (m *mockStore) UpdateAssumptionEmbedding(_ context.Context, id int64, embedding pgvector.Vector, _ string) error {
	if m.failID != 0 && m.failID == id {
		return errors.New("mock store error")
	}
	if m.stored == nil {
		m.stored = make(map[int64]pgvector.Vector)
	}
	m.stored[id] = embedding
	return nil
}

func makeAssumption(id int64, text string, hints []string) *store.Assumption {
	return &store.Assumption{
		ID:            id,
		Text:          text,
		EvidenceHints: hints,
	}
}

func newEmbedder(table map[string][]float64, st assumptionUpdater) *AssumptionEmbedder {
	return &AssumptionEmbedder{
		embedder:  &mockEmbedder{table: table},
		model:     "test-model",
		batchSize: 64,
		store:     st,
	}
}

func TestEmbedAssumptions_StoresVectors(t *testing.T) {
	text := "iPhone revenue grows strongly"
	emb := []float64{1, 0, 0, 0}
	table := map[string][]float64{text: emb}
	st := &mockStore{}

	e := newEmbedder(table, st)
	assumptions := []*store.Assumption{makeAssumption(1, text, nil)}

	if err := e.EmbedAssumptions(context.Background(), assumptions); err != nil {
		t.Fatal(err)
	}
	vec, ok := st.stored[1]
	if !ok {
		t.Fatal("embedding not stored for assumption 1")
	}
	slice := vec.Slice()
	if len(slice) != 4 || slice[0] != 1 {
		t.Errorf("unexpected vector: %v", slice)
	}
}

func TestEmbedAssumptions_IncludesEvidenceHints(t *testing.T) {
	// The embed text should concatenate text + hints; ensure correct key is used.
	text := "Services margin expands"
	hints := []string{"services revenue", "gross margin"}
	embedText := text + " " + "services revenue gross margin"
	emb := []float64{0, 1, 0, 0}
	table := map[string][]float64{embedText: emb}
	st := &mockStore{}

	e := newEmbedder(table, st)
	assumptions := []*store.Assumption{makeAssumption(2, text, hints)}

	if err := e.EmbedAssumptions(context.Background(), assumptions); err != nil {
		t.Fatal(err)
	}
	vec, ok := st.stored[2]
	if !ok {
		t.Fatal("embedding not stored")
	}
	if vec.Slice()[1] != 1 {
		t.Errorf("unexpected vector: %v", vec.Slice())
	}
}

func TestEmbedAssumptions_SkipsAlreadyEmbedded(t *testing.T) {
	st := &mockStore{}
	mock := &mockEmbedder{table: map[string][]float64{}}
	e := &AssumptionEmbedder{embedder: mock, model: "test-model", batchSize: 64, store: st}

	existing := pgvector.NewVector([]float32{1, 0, 0, 0})
	a := makeAssumption(3, "some text", nil)
	a.Embedding = &existing
	a.EmbeddingModel = "test-model"

	if err := e.EmbedAssumptions(context.Background(), []*store.Assumption{a}); err != nil {
		t.Fatal(err)
	}
	if mock.callCount != 0 {
		t.Errorf("expected no embed call, got %d", mock.callCount)
	}
	if len(st.stored) != 0 {
		t.Error("expected nothing stored for already-embedded assumption")
	}
}

func TestEmbedAssumptions_ReembedsDifferentModel(t *testing.T) {
	text := "Mac shipments decline"
	emb := []float64{0, 0, 1, 0}
	table := map[string][]float64{text: emb}
	st := &mockStore{}
	e := newEmbedder(table, st)

	existing := pgvector.NewVector([]float32{1, 0, 0, 0})
	a := makeAssumption(4, text, nil)
	a.Embedding = &existing
	a.EmbeddingModel = "old-model" // different model → must re-embed

	if err := e.EmbedAssumptions(context.Background(), []*store.Assumption{a}); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.stored[4]; !ok {
		t.Error("expected re-embed for different model")
	}
}

func TestEmbedAssumptions_Empty(t *testing.T) {
	st := &mockStore{}
	e := newEmbedder(nil, st)
	if err := e.EmbedAssumptions(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(st.stored) != 0 {
		t.Error("expected no store calls for empty input")
	}
}

func TestEmbedAssumptions_EmbedError(t *testing.T) {
	mock := &mockEmbedder{table: map[string][]float64{}, failAfter: 1}
	e := &AssumptionEmbedder{embedder: mock, model: "test-model", batchSize: 64, store: &mockStore{}}
	a := makeAssumption(5, "some text", nil)
	if err := e.EmbedAssumptions(context.Background(), []*store.Assumption{a}); err == nil {
		t.Fatal("expected error from embed failure")
	}
}

func TestEmbedAssumptions_StoreError(t *testing.T) {
	text := "revenue grows"
	table := map[string][]float64{text: {1, 0, 0, 0}}
	st := &mockStore{failID: 6}
	e := newEmbedder(table, st)
	a := makeAssumption(6, text, nil)
	if err := e.EmbedAssumptions(context.Background(), []*store.Assumption{a}); err == nil {
		t.Fatal("expected error from store failure")
	}
}

func TestEmbedAssumptions_Batching(t *testing.T) {
	table := make(map[string][]float64)
	assumptions := make([]*store.Assumption, 5)
	for i := range assumptions {
		text := "assumption text " + string(rune('A'+i))
		assumptions[i] = makeAssumption(int64(i+10), text, nil)
		table[text] = []float64{float64(i), 0, 0, 0}
	}
	mock := &mockEmbedder{table: table}
	st := &mockStore{}
	e := &AssumptionEmbedder{embedder: mock, model: "test-model", batchSize: 2, store: st}

	if err := e.EmbedAssumptions(context.Background(), assumptions); err != nil {
		t.Fatal(err)
	}
	// 5 assumptions / batchSize 2 = 3 calls
	if mock.callCount != 3 {
		t.Errorf("callCount = %d, want 3", mock.callCount)
	}
	if len(st.stored) != 5 {
		t.Errorf("stored %d, want 5", len(st.stored))
	}
}

func TestAssumptionEmbedText_NoHints(t *testing.T) {
	a := &store.Assumption{Text: "revenue grows"}
	if got := assumptionEmbedText(a); got != "revenue grows" {
		t.Errorf("got %q", got)
	}
}

func TestAssumptionEmbedText_WithHints(t *testing.T) {
	a := &store.Assumption{
		Text:          "revenue grows",
		EvidenceHints: []string{"quarterly revenue", "YoY growth"},
	}
	want := "revenue grows quarterly revenue YoY growth"
	if got := assumptionEmbedText(a); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
