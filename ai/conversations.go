package ai

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Skookum-Infotech/go-rag/rag"
)

// historyLimit bounds how many of a conversation's most recent messages are
// loaded into a turn's prompt. Unbounded history would grow every turn's
// prefill cost without bound on a CPU-bound box; 20 messages (10 turns) is
// enough realistic follow-up context ("what about last month?") without a
// long-lived conversation's prompt growing forever.
const historyLimit = 20

// Conversation is one AI assistant chat thread, owned by exactly one user.
type Conversation struct {
	ID          string
	OwnerUserID string
	Title       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Message is one stored turn: plain role+content text only, never citations
// or retrieved chunks — see the ai_conversations/ai_messages schema doc
// comment on why (grounding always re-retrieves fresh per turn).
type Message struct {
	Role      string
	Content   string
	CreatedAt time.Time
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

// Get loads one conversation by id. found is false if it doesn't exist.
// Callers MUST check OwnerUserID before trusting the result — see the
// package doc.
func (s *ConversationStore) Get(ctx context.Context, id string) (c Conversation, found bool, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT id, owner_user_id, title, created_at, updated_at
		FROM ai_conversations WHERE id = $1`, id,
	).Scan(&c.ID, &c.OwnerUserID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
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
		return fmt.Errorf("delete conversation: %w", err)
	}
	return nil
}

// SetTitleIfEmpty sets a conversation's title the first time one is needed
// (typically its first question, truncated by the caller) — a no-op if a
// title is already set, so a caller can call this unconditionally on every
// turn without overwriting an existing title.
func (s *ConversationStore) SetTitleIfEmpty(ctx context.Context, id, title string) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE ai_conversations SET title = $2 WHERE id = $1 AND title = ''`,
		id, title); err != nil {
		return fmt.Errorf("set conversation title: %w", err)
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
		SELECT role, content, created_at FROM ai_messages
		WHERE conversation_id = $1 ORDER BY created_at ASC`, conversationID)
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

// History returns up to historyLimit of a conversation's most recent
// messages, oldest first, as rag.Message — ready for
// Assistant.Ask/rag.AskRequest.History. Bounded so a long-lived
// conversation's prompt doesn't grow unboundedly (see historyLimit).
func (s *ConversationStore) History(ctx context.Context, conversationID string) ([]rag.Message, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT role, content FROM (
			SELECT role, content, created_at FROM ai_messages
			WHERE conversation_id = $1
			ORDER BY created_at DESC LIMIT $2
		) recent ORDER BY created_at ASC`, conversationID, historyLimit)
	if err != nil {
		return nil, fmt.Errorf("load conversation history: %w", err)
	}
	defer rows.Close()
	out := []rag.Message{}
	for rows.Next() {
		var m rag.Message
		if err := rows.Scan(&m.Role, &m.Content); err != nil {
			return nil, fmt.Errorf("scan ai message: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
