package ai

// Chunk is one embeddable unit persisted to rag_chunks. It lives in this
// dependency-free package (not ai/index, which imports ai) so that a type
// implementing the ai/index.ChunkSink interface — e.g. RagStore — never has
// to import ai/index and risk a cycle.
type Chunk struct {
	SourceID, WorkflowID, OwnerUserID, TeamID, Content, ContentHash string
	Embedding                                                       []float32
}
