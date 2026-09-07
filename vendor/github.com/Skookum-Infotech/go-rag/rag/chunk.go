package rag

// Chunk is one embeddable unit persisted to rag_chunks. It lives in this
// dependency-free package (not ai/index, which imports ai) so that a type
// implementing the ai/index.ChunkSink interface — e.g. RagStore — never has
// to import ai/index and risk a cycle.
type Chunk struct {
	SourceID, WorkflowID, OwnerUserID, TeamID, Content, ContentHash string
	Embedding                                                       []float32
}

// ChunkMeta is the stored bookkeeping for one indexed source: what its text
// hashed to, and the scope columns retrieval narrows on. It is deliberately
// everything-except-the-embedding, so an index worker can decide whether a
// re-embed is needed without reading a wide vector back.
//
// Note the asymmetry that makes this subtle: ContentHash covers the rendered
// text and the embedder that produced the vector (see VectorHash), but NOT the
// scope columns — they travel beside the text rather than in it. A record can
// therefore change hands with its hash completely unchanged, which is why a
// hash-only skip leaves stale scope behind.
type ChunkMeta struct {
	ContentHash string
	WorkflowID  string
	OwnerUserID string
	TeamID      string
}
