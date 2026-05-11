package rag

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"alphathesis/client"
)

const (
	defaultChunkSize    = 400  // bytes; roughly 100–150 tokens for English, ~130 for Chinese
	defaultBatchSize    = 64   // max chunks per vLLM /embeddings call
	defaultTopK         = 3
	defaultMinSimilarity = 0.5 // cosine similarity threshold; chunks below this are dropped
)

// Embedder is the subset of client.VLLMClient that RAGEngine needs.
type Embedder interface {
	CreateEmbedding(ctx context.Context, req client.EmbeddingRequest) (*client.EmbeddingResponse, error)
}

// AssumptionInput carries the assumption identifier and its pre-computed
// embedding vector. The caller loads these from the assumptions table.
type AssumptionInput struct {
	ID        int64
	Embedding []float64
}

// RAGEngine performs in-memory RAG: chunk → embed → cosine recall.
// Nothing is written to the database; all intermediate state lives in memory
// for the duration of one Recall call.
type RAGEngine struct {
	embedder      Embedder
	model         string
	chunkSize     int
	batchSize     int
	minSimilarity float64
}

type Option func(*RAGEngine)

func WithChunkSize(n int) Option {
	return func(e *RAGEngine) {
		if n > 0 {
			e.chunkSize = n
		}
	}
}

func WithBatchSize(n int) Option {
	return func(e *RAGEngine) {
		if n > 0 {
			e.batchSize = n
		}
	}
}

func WithMinSimilarity(threshold float64) Option {
	return func(e *RAGEngine) {
		if threshold >= 0 && threshold <= 1 {
			e.minSimilarity = threshold
		}
	}
}

func New(embedder Embedder, model string, opts ...Option) *RAGEngine {
	e := &RAGEngine{
		embedder:      embedder,
		model:         model,
		chunkSize:     defaultChunkSize,
		batchSize:     defaultBatchSize,
		minSimilarity: defaultMinSimilarity,
	}
	for _, o := range opts {
		if o != nil {
			o(e)
		}
	}
	return e
}

// Recall chunks fullText, embeds all chunks via vLLM, and returns the top-k
// most similar chunk texts for each assumption.
//
// Assumptions with a zero-length embedding are skipped.
// If fullText is empty or produces no chunks, an empty map is returned (no error).
func (e *RAGEngine) Recall(
	ctx context.Context,
	fullText string,
	assumptions []AssumptionInput,
	topK int,
) (map[int64][]string, error) {
	if topK <= 0 {
		topK = defaultTopK
	}
	if strings.TrimSpace(fullText) == "" || len(assumptions) == 0 {
		return map[int64][]string{}, nil
	}

	chunks := chunkText(fullText, e.chunkSize)
	if len(chunks) == 0 {
		return map[int64][]string{}, nil
	}

	chunkEmbs, err := e.embedTexts(ctx, chunks)
	if err != nil {
		return nil, err
	}

	result := make(map[int64][]string, len(assumptions))
	for _, a := range assumptions {
		if len(a.Embedding) == 0 {
			continue
		}
		idxs := topKByCosine(a.Embedding, chunkEmbs, topK, e.minSimilarity)
		if len(idxs) == 0 {
			continue // no chunk met the threshold; assumption falls back to summary mode
		}
		texts := make([]string, len(idxs))
		for i, idx := range idxs {
			texts[i] = chunks[idx]
		}
		result[a.ID] = texts
	}
	return result, nil
}

// embedTexts calls the embedding model in batches and returns one embedding
// vector per input text, preserving order via the Index field in the response.
func (e *RAGEngine) embedTexts(ctx context.Context, texts []string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for start := 0; start < len(texts); start += e.batchSize {
		end := min(start+e.batchSize, len(texts))
		batch := texts[start:end]

		resp, err := e.embedder.CreateEmbedding(ctx, client.EmbeddingRequest{
			Model: e.model,
			Input: batch,
		})
		if err != nil {
			return nil, fmt.Errorf("rag embed batch [%d:%d]: %w", start, end, err)
		}
		for _, d := range resp.Data {
			absIdx := start + d.Index
			if absIdx < 0 || absIdx >= len(out) {
				continue
			}
			out[absIdx] = d.Embedding
		}
	}
	return out, nil
}

// topKByCosine returns the indices of the top-k candidates most similar to
// query, ranked by cosine similarity (descending).
func topKByCosine(query []float64, candidates [][]float64, k int, minSim float64) []int {
	type entry struct {
		idx int
		sim float64
	}

	qn := vecNorm(query)
	if qn == 0 {
		return nil
	}

	scored := make([]entry, 0, len(candidates))
	for i, emb := range candidates {
		if len(emb) == 0 {
			continue
		}
		cn := vecNorm(emb)
		if cn == 0 {
			continue
		}
		if sim := vecDot(query, emb) / (qn * cn); sim >= minSim {
			scored = append(scored, entry{i, sim})
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].sim > scored[j].sim
	})

	k = min(k, len(scored))
	idxs := make([]int, k)
	for i := range k {
		idxs[i] = scored[i].idx
	}
	return idxs
}

func vecDot(a, b []float64) float64 {
	n := min(len(a), len(b))
	var s float64
	for i := range n {
		s += a[i] * b[i]
	}
	return s
}

func vecNorm(v []float64) float64 {
	var s float64
	for _, x := range v {
		s += x * x
	}
	return math.Sqrt(s)
}
