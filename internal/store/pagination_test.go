package store

import (
	"testing"
	"time"
)

type paginationTestItem struct {
	createdAt time.Time
	id        string
}

func paginationTestItemCursor(item paginationTestItem) TimeIDCursor {
	return TimeIDCursor{Timestamp: item.createdAt, ID: item.id}
}

func TestFinalizeTimeIDPageWithoutLookahead(t *testing.T) {
	timestamp := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	fetchedItems := []paginationTestItem{
		{createdAt: timestamp, id: "first"},
		{createdAt: timestamp.Add(time.Second), id: "second"},
	}

	items, hasMore, nextCursor := finalizeTimeIDPage(fetchedItems, 2, paginationTestItemCursor)
	if len(items) != 2 {
		t.Fatalf("returned item count = %d, want 2", len(items))
	}
	if hasMore {
		t.Fatal("exact-limit page unexpectedly reported more items")
	}
	if nextCursor != nil {
		t.Fatalf("exact-limit page next cursor = %+v, want nil", nextCursor)
	}
}

func TestFinalizeTimeIDPageTruncatesLookaheadAndBuildsCursor(t *testing.T) {
	timestamp := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	fetchedItems := []paginationTestItem{
		{createdAt: timestamp, id: "first"},
		{createdAt: timestamp.Add(time.Second), id: "second"},
		{createdAt: timestamp.Add(2 * time.Second), id: "lookahead"},
	}

	items, hasMore, nextCursor := finalizeTimeIDPage(fetchedItems, 2, paginationTestItemCursor)
	if len(items) != 2 {
		t.Fatalf("returned item count = %d, want 2", len(items))
	}
	if !hasMore {
		t.Fatal("lookahead page did not report more items")
	}
	if nextCursor == nil {
		t.Fatal("lookahead page did not return a next cursor")
	}
	if nextCursor.ID != "second" || !nextCursor.Timestamp.Equal(timestamp.Add(time.Second)) {
		t.Fatalf("next cursor = %+v, want cursor for second returned item", nextCursor)
	}
}

func TestTimeIDCursorPredicateUsesRequestedDirection(t *testing.T) {
	if predicate := timeIDCursorPredicate(timeIDAscending); predicate != "(created_at > ? OR (created_at = ? AND id > ?))" {
		t.Fatalf("ascending predicate = %q", predicate)
	}
	if predicate := timeIDCursorPredicate(timeIDDescending); predicate != "(created_at < ? OR (created_at = ? AND id < ?))" {
		t.Fatalf("descending predicate = %q", predicate)
	}
}
