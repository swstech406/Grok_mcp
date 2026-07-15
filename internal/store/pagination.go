package store

type timeIDSortDirection uint8

const (
	timeIDAscending timeIDSortDirection = iota
	timeIDDescending
)

func timeIDCursorPredicate(direction timeIDSortDirection) string {
	comparisonOperator := ">"
	if direction == timeIDDescending {
		comparisonOperator = "<"
	}

	return "(created_at " + comparisonOperator + " ? OR (created_at = ? AND id " + comparisonOperator + " ?))"
}

func appendTimeIDCursorArguments(queryArguments []any, cursor *TimeIDCursor) []any {
	cursorTimestamp := formatTime(cursor.Timestamp.UTC())
	return append(queryArguments, cursorTimestamp, cursorTimestamp, cursor.ID)
}

func keysetFetchLimit(pageLimit int) int {
	return pageLimit + 1
}

func finalizeTimeIDPage[Item any](
	fetchedItems []Item,
	pageLimit int,
	cursorForItem func(Item) TimeIDCursor,
) (items []Item, hasMore bool, nextCursor *TimeIDCursor) {
	if len(fetchedItems) <= pageLimit {
		return fetchedItems, false, nil
	}

	items = fetchedItems[:pageLimit]
	lastItemCursor := cursorForItem(items[len(items)-1])
	return items, true, &lastItemCursor
}
