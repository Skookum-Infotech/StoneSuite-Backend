//go:build dbtest

package ai

import (
	"fmt"
	"testing"
	"time"
)

const (
	convUserA = "cccccccc-0000-0000-0000-000000000001"
	convUserB = "cccccccc-0000-0000-0000-000000000002"
)

func TestConversationStore_CreateAndGetRoundTrip(t *testing.T) {
	pool := newTestPool(t)
	s := NewConversationStore(pool)
	ctx := ctxS(t)

	created, err := s.Create(ctx, convUserA)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.OwnerUserID != convUserA || created.Title != "" {
		t.Fatalf("unexpected Create result: %+v", created)
	}

	got, found, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected found=true right after Create")
	}
	if got.ID != created.ID || got.OwnerUserID != convUserA {
		t.Fatalf("Get = %+v, want it to match Create's result", got)
	}
}

func TestConversationStore_GetMissingReturnsNotFound(t *testing.T) {
	pool := newTestPool(t)
	s := NewConversationStore(pool)
	_, found, err := s.Get(ctxS(t), "00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected found=false for a nonexistent id")
	}
}

func TestConversationStore_ListByOwnerOnlyReturnsOwnAndOrdersByRecentActivity(t *testing.T) {
	pool := newTestPool(t)
	s := NewConversationStore(pool)
	ctx := ctxS(t)

	older, err := s.Create(ctx, convUserA)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := s.Create(ctx, convUserA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, convUserB); err != nil {
		t.Fatal(err)
	}

	// Touch `older` so it becomes the most recently active, proving order
	// tracks activity (AppendMessage's updated_at bump), not creation time.
	time.Sleep(10 * time.Millisecond)
	if err := s.AppendMessage(ctx, older.ID, "user", "hello again"); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListByOwner(ctx, convUserA)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("ListByOwner returned %d conversations, want exactly userA's 2 (never userB's)", len(list))
	}
	if list[0].ID != older.ID || list[1].ID != newer.ID {
		t.Fatalf("ListByOwner order = [%s, %s], want the touched conversation (%s) first", list[0].ID, list[1].ID, older.ID)
	}
}

func TestConversationStore_AppendMessageAndHistoryRoundTrip(t *testing.T) {
	pool := newTestPool(t)
	s := NewConversationStore(pool)
	ctx := ctxS(t)

	conv, err := s.Create(ctx, convUserA)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(ctx, conv.ID, "user", "first question"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(ctx, conv.ID, "assistant", "first answer"); err != nil {
		t.Fatal(err)
	}

	history, err := s.History(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("History returned %d messages, want 2", len(history))
	}
	if history[0].Role != "user" || history[0].Content != "first question" {
		t.Fatalf("history[0] = %+v", history[0])
	}
	if history[1].Role != "assistant" || history[1].Content != "first answer" {
		t.Fatalf("history[1] = %+v", history[1])
	}
}

// TestConversationStore_HistoryIsBoundedToMostRecent proves a long-lived
// conversation's prompt doesn't grow unboundedly: History returns at most
// historyLimit messages, and it's the MOST RECENT ones, in chronological
// order — never an arbitrary or oldest-first slice.
func TestConversationStore_HistoryIsBoundedToMostRecent(t *testing.T) {
	pool := newTestPool(t)
	s := NewConversationStore(pool)
	ctx := ctxS(t)

	conv, err := s.Create(ctx, convUserA)
	if err != nil {
		t.Fatal(err)
	}
	total := historyLimit + 5
	for i := 0; i < total; i++ {
		if err := s.AppendMessage(ctx, conv.ID, "user", fmt.Sprintf("turn %d", i)); err != nil {
			t.Fatal(err)
		}
	}

	history, err := s.History(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != historyLimit {
		t.Fatalf("History returned %d messages, want exactly historyLimit=%d", len(history), historyLimit)
	}
	// The oldest surviving message must be turn (total-historyLimit), and
	// order must still be chronological (ascending), not reversed.
	wantFirst := fmt.Sprintf("turn %d", total-historyLimit)
	if history[0].Content != wantFirst {
		t.Fatalf("history[0].Content = %q, want %q (the oldest of the most recent historyLimit turns)", history[0].Content, wantFirst)
	}
	wantLast := fmt.Sprintf("turn %d", total-1)
	if history[len(history)-1].Content != wantLast {
		t.Fatalf("history[last].Content = %q, want %q (the most recent turn)", history[len(history)-1].Content, wantLast)
	}
}

func TestConversationStore_MessagesReturnsFullUnboundedTranscript(t *testing.T) {
	pool := newTestPool(t)
	s := NewConversationStore(pool)
	ctx := ctxS(t)

	conv, err := s.Create(ctx, convUserA)
	if err != nil {
		t.Fatal(err)
	}
	total := historyLimit + 5
	for i := 0; i < total; i++ {
		if err := s.AppendMessage(ctx, conv.ID, "user", fmt.Sprintf("turn %d", i)); err != nil {
			t.Fatal(err)
		}
	}

	messages, err := s.Messages(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != total {
		t.Fatalf("Messages returned %d, want the full %d-message transcript (unlike History, it must not be bounded)", len(messages), total)
	}
}

func TestConversationStore_SetTitleIfEmptyDoesNotOverwrite(t *testing.T) {
	pool := newTestPool(t)
	s := NewConversationStore(pool)
	ctx := ctxS(t)

	conv, err := s.Create(ctx, convUserA)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTitleIfEmpty(ctx, conv.ID, "first title"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTitleIfEmpty(ctx, conv.ID, "second title, should be ignored"); err != nil {
		t.Fatal(err)
	}

	got, found, err := s.Get(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if got.Title != "first title" {
		t.Fatalf("Title = %q, want the FIRST title to stick", got.Title)
	}
}

// TestConversationStore_DeleteCascadesMessages proves the FK's ON DELETE
// CASCADE actually fires: deleting a conversation must not leave orphaned
// ai_messages rows behind.
func TestConversationStore_DeleteCascadesMessages(t *testing.T) {
	pool := newTestPool(t)
	s := NewConversationStore(pool)
	ctx := ctxS(t)

	conv, err := s.Create(ctx, convUserA)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(ctx, conv.ID, "user", "hello"); err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(ctx, conv.ID); err != nil {
		t.Fatal(err)
	}

	_, found, err := s.Get(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected the conversation to be gone after Delete")
	}

	var orphaned int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM ai_messages WHERE conversation_id = $1`, conv.ID).Scan(&orphaned); err != nil {
		t.Fatal(err)
	}
	if orphaned != 0 {
		t.Fatalf("found %d orphaned ai_messages rows after deleting their conversation", orphaned)
	}
}

func TestConversationStore_DeleteMissingIsNoop(t *testing.T) {
	pool := newTestPool(t)
	s := NewConversationStore(pool)
	if err := s.Delete(ctxS(t), "00000000-0000-0000-0000-000000000000"); err != nil {
		t.Fatalf("Delete of a nonexistent id must be a no-op, not an error: %v", err)
	}
}
