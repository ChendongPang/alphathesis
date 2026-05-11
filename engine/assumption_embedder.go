package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/pgvector/pgvector-go"
	"alphathesis/client"
	"alphathesis/engine/rag"
	"alphathesis/store"
)

const defaultAssumptionEmbedBatchSize = 64

// assumptionUpdater is the store subset AssumptionEmbedder needs.
type assumptionUpdater interface {
	UpdateAssumptionEmbedding(ctx context.Context, id int64, embedding pgvector.Vector, model string) error
}

// AssumptionEmbedder embeds assumption texts and persists vectors to the DB.
// It is called once after a thesis is created or updated.
type AssumptionEmbedder struct {
	embedder  rag.Embedder
	model     string
	batchSize int
	store     assumptionUpdater
}

// NewAssumptionEmbedder creates an embedder backed by the given embedding model.
func NewAssumptionEmbedder(embedder rag.Embedder, model string, thesisStore *store.ThesisStore) *AssumptionEmbedder {
	return &AssumptionEmbedder{
		embedder:  embedder,
		model:     model,
		batchSize: defaultAssumptionEmbedBatchSize,
		store:     thesisStore,
	}
}

// EmbedAssumptions embeds any assumption that lacks a vector for the current
// model and writes the result back to the DB. Assumptions already embedded
// with the same model are skipped.
func (e *AssumptionEmbedder) EmbedAssumptions(ctx context.Context, assumptions []*store.Assumption) error {
	var pending []*store.Assumption
	var texts []string
	for _, a := range assumptions {
		if a.Embedding != nil && a.EmbeddingModel == e.model {
			continue
		}
		pending = append(pending, a)
		texts = append(texts, assumptionEmbedText(a))
	}
	if len(pending) == 0 {
		return nil
	}

	embeddings := make([][]float64, len(texts))
	for start := 0; start < len(texts); start += e.batchSize {
		end := start + e.batchSize
		if end > len(texts) {
			end = len(texts)
		}
		resp, err := e.embedder.CreateEmbedding(ctx, client.EmbeddingRequest{
			Model: e.model,
			Input: texts[start:end],
		})
		if err != nil {
			return fmt.Errorf("embed assumptions [%d:%d]: %w", start, end, err)
		}
		for _, d := range resp.Data {
			idx := start + d.Index
			if idx >= 0 && idx < len(embeddings) {
				embeddings[idx] = d.Embedding
			}
		}
	}

	for i, a := range pending {
		emb := embeddings[i]
		if len(emb) == 0 {
			continue
		}
		vec := pgvector.NewVector(toFloat32Slice(emb))
		if err := e.store.UpdateAssumptionEmbedding(ctx, a.ID, vec, e.model); err != nil {
			return fmt.Errorf("store embedding for assumption %d: %w", a.ID, err)
		}
	}
	return nil
}

// assumptionEmbedText builds the text to embed: assumption text + evidence hints.
// Including hints gives the vector richer signal for RAG retrieval because hints
// describe the kind of evidence we expect to find in articles.
func assumptionEmbedText(a *store.Assumption) string {
	if len(a.EvidenceHints) == 0 {
		return a.Text
	}
	return a.Text + " " + strings.Join(a.EvidenceHints, " ")
}

func toFloat32Slice(v []float64) []float32 {
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(x)
	}
	return out
}
