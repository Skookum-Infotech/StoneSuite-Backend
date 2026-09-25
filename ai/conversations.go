package ai

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Skookum-Infotech/go-rag/rag"
)

// historyLimit bounds how many of a conversation's most recent messages are
// loaded into a turn's prompt. Unbounded history would grow every turn's
// prefill cost without bound on a CPU-bound box; 20 messages (10 turns) is
// enough realistic follow-up context ("what about last month?") without a
// long-lived conversation's prompt growing forever.
const historyLimit = 20

// maxTranscriptMessages caps Messages (the transcript shown when a user
// reopens a conversation) to the most recent messages, so a very long
// conversation can't produce an unbounded response.
const maxTranscriptMessages = 200

// Conversation is one AI assistant chat thread, owned by exactly one user.
type Conversation struct {
	ID          string    `json:"id"`
	OwnerUserID string    `json:"ownerUserId"`
	Title       string    `json:"title"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Message is one stored turn: plain role+content text only, never citations
// or retrieved chunks — see the ai_conversations/ai_messages schema doc
// comment on why (grounding always re-retrieves fresh per turn).
type Message struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
}

// ConversationStore persists AI assistant conversation history.
//
// Ownership here is the caller's own internal user id (the same identity
// ai_conversations.owner_user_id / rag_chunks.owner_user_id /
// workflow_records.owner_user_id all use — see controllers/ai.go's
// workflow.UserIDByIdentity call), never RBAC scope: a conversation is
// personal chat history, visible only to the user who started it, regardless
// of what "all"/"own" CRM scope they hold.
//
// Get/List/Delete do NOT themselves enforce ownership — every caller MUST
// compare Conversation.OwnerUserID against the caller before trusting or
// acting on a result, returning 404 (never 403) on any mismatch, the same
// IDOR-safe convention as recordInScope elsewhere in this codebase. Keeping
// the check at the caller means it happens once, in the same place request
// context is already available, rather than threading a caller id through
// every store method.
type ConversationStore struct{ pool *pgxpool.Pool }

// NewConversationStore builds a store over a tenant pool.
func NewConversationStore(pool *pgxpool.Pool) *ConversationStore {
	return &ConversationStore{pool: pool}
}

// Create starts a new, empty, untitled conversation owned by ownerUserID.
func (s *ConversationStore) Create(ctx context.Context, ownerUserID string) (Conversation, error) {
	var c Conversation
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ai_conversations (owner_user_id) VALUES ($1)
		RETURNING id, owner_user_id, title, created_at, updated_at`,
		ownerUserID,
	).Scan(&c.ID, &c.OwnerUserID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return Conversation{}, fmt.Errorf("create conversation: %w", err)
	}
	return c, nil
}

// notFound reports whether err means "this conversation isn't there", covering
// both a genuinely absent row and an id that isn't a well-formed uuid.
//
// The 22P02 case matters: ai_conversations.id is a uuid column, so a client
// sending garbage in the path makes Postgres reject the literal rather than
// return zero rows. Rendering that as a 500 both leaks that the id was
// malformed (a 404 for a real-looking id vs a 500 for garbage is an oracle)
// and pages us for ordinary client noise. Same convention, and same reasoning,
// as inventoryadjustment/store.go's isInvalidTextRepresentation.
func notFound(err error) bool {
	if errors.Is(err, pgx.ErrNoRows) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "22P02"
}

// Get loads one conversation by id. found is false if it doesn't exist or the
// id is not a well-formed uuid. Callers MUST check OwnerUserID before trusting
// the result — see the package doc.
func (s *ConversationStore) Get(ctx context.Context, id string) (c Conversation, found bool, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT id, owner_user_id, title, created_at, updated_at
		FROM ai_conversations WHERE id = $1`, id,
	).Scan(&c.ID, &c.OwnerUserID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if notFound(err) {
			return Conversation{}, false, nil
		}
		return Conversation{}, false, fmt.Errorf("get conversation: %w", err)
	}
	return c, true, nil
}

// ListByOwner returns ownerUserID's own conversations, most recently active
// first.
func (s *ConversationStore) ListByOwner(ctx context.Context, ownerUserID string) ([]Conversation, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, owner_user_id, title, created_at, updated_at
		FROM ai_conversations WHERE owner_user_id = $1
		ORDER BY updated_at DESC`, ownerUserID)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.ID, &c.OwnerUserID, &c.Title, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan conversation: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Delete removes a conversation and its messages (ai_messages cascades on
// conversation_id). A no-op, not an error, if id doesn't exist. Callers MUST
// enforce ownership before calling this — see the package doc.
func (s *ConversationStore) Delete(ctx context.Context, id string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM ai_conversations WHERE id = $1`, id); err != nil {
		if notFound(err) {
			return nil
		}
		return fmt.Errorf("delete conversation: %w", err)
	}
	return nil
}

// AppendMessage records one turn (role is "user" or "assistant", matching the
// table's CHECK constraint) and bumps the conversation's updated_at in the
// same round trip, so ListByOwner's "most recently active first" order stays
// correct without a second query.
func (s *ConversationStore) AppendMessage(ctx context.Context, conversationID, role, content string) error {
	_, err := s.pool.Exec(ctx, `
		WITH ins AS (
			INSERT INTO ai_messages (conversation_id, role, content)
			VALUES ($1, $2, $3)
			RETURNING conversation_id
		)
		UPDATE ai_conversations SET updated_at = NOW()
		WHERE id = (SELECT conversation_id FROM ins)`,
		conversationID, role, content)
	if err != nil {
		return fmt.Errorf("append ai message: %w", err)
	}
	return nil
}

// Messages returns every message in a conversation, oldest first, with
// timestamps — for display (e.g. GET one conversation), unlike History
// which is bounded and timestamp-free for feeding straight to the LLM.
func (s *ConversationStore) Messages(ctx context.Context, conversationID string) ([]Message, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT role, content, created_at FROM (
			SELECT role, content, created_at FROM ai_messages
			WHERE conversation_id = $1 ORDER BY created_at DESC LIMIT $2
		) recent ORDER BY created_at ASC`, conversationID, maxTranscriptMessages)
	if err != nil {
		return nil, fmt.Errorf("list conversation messages: %w", err)
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan ai message: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// historyCharBudget caps how much prior conversation is replayed into the
// prompt (~4 chars per token, so ~2000 tokens). historyLimit alone let 20
// messages of up to 2000 bytes each reach a 4096-token context window, where
// Ollama silently drops the oldest tokens — the system prompt first.
const historyCharBudget = 8000

// History returns the most recent turns of a conversation, oldest first, as
// model messages: at most historyLimit messages and at most historyCharBudget
// characters, dropping the oldest first. Always ends on a whole turn boundary
// it can keep — a single message larger than the whole budget is dropped
// rather than truncated mid-sentence.
func (s *ConversationStore) History(ctx context.Context, conversationID string) ([]rag.Message, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT role, content FROM ai_messages
		WHERE conversation_id = $1
		ORDER BY created_at DESC LIMIT $2`, conversationID, historyLimit)
	if err != nil {
		return nil, fmt.Errorf("load conversation history: %w", err)
	}
	defer rows.Close()
	var newestFirst []rag.Message
	used := 0
	for rows.Next() {
		var m rag.Message
		if err := rows.Scan(&m.Role, &m.Content); err != nil {
			return nil, fmt.Errorf("scan ai message: %w", err)
		}
		if used+len(m.Content) > historyCharBudget {
			break
		}
		used += len(m.Content)
		newestFirst = append(newestFirst, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load conversation history: %w", err)
	}
	out := make([]rag.Message, len(newestFirst))
	for i, m := range newestFirst {
		out[len(newestFirst)-1-i] = m
	}
	return out, nil
}

// AppendTurn records one completed question/answer pair and sets the
// conversation's title from title if it has none — all in one transaction, so
// a conversation never holds a question without its answer (which the next
// turn would replay as an unanswered question). clock_timestamp(), not NOW():
// NOW() is fixed for the whole transaction, which would tie the two rows'
// created_at and make their order undefined.
func (s *ConversationStore) AppendTurn(ctx context.Context, conversationID, question, answer, title string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("append turn: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, m := range []struct{ role, content string }{{"user", question}, {"assistant", answer}} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO ai_messages (conversation_id, role, content, created_at)
			VALUES ($1, $2, $3, clock_timestamp())`, conversationID, m.role, m.content); err != nil {
			return fmt.Errorf("append turn: insert %s message: %w", m.role, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE ai_conversations
		SET updated_at = NOW(), title = CASE WHEN title = '' THEN $2 ELSE title END
		WHERE id = $1`, conversationID, title); err != nil {
		return fmt.Errorf("append turn: touch conversation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("append turn: commit: %w", err)
	}
	return nil
}

// DeleteIdle removes conversations (and, by cascade, their messages) with no
// activity for longer than maxIdle, returning how many were removed. Chat
// history can quote record data the caller has since lost access to, so it is
// not kept forever.
func (s *ConversationStore) DeleteIdle(ctx context.Context, maxIdle time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM ai_conversations WHERE updated_at < NOW() - make_interval(secs => $1)`, maxIdle.Seconds())
	if err != nil {
		return 0, fmt.Errorf("delete idle conversations: %w", err)
	}
	return tag.RowsAffected(), nil
}
