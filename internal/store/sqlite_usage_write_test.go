package store

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestRecordUsageBatchPersistsRowsAndCoalescesAPIKeyUpdates(t *testing.T) {
	sqliteStore := openTestDB(t)
	sqliteStore.SetMetricsEnabled(true)
	ctx := context.Background()
	userID := testUserID(t, sqliteStore)
	apiKey, _, err := sqliteStore.CreateKey(ctx, userID, "batch-usage-key", 20)
	if err != nil {
		t.Fatal(err)
	}

	latestTimestamp := time.Now().UTC().Truncate(time.Second)
	records := []UsageRecord{
		{KeyID: apiKey.ID, ToolName: "grok_web_search", Timestamp: latestTimestamp.Add(-time.Minute), DurationMs: 10, Success: true},
		{KeyID: apiKey.ID, ToolName: "grok_x_search", Timestamp: latestTimestamp, DurationMs: 20, Success: false},
		{KeyID: apiKey.ID, ToolName: "grok_web_search", Timestamp: latestTimestamp.Add(-30 * time.Second), DurationMs: 30, Success: true},
	}
	if err := sqliteStore.RecordUsageBatch(ctx, records); err != nil {
		t.Fatalf("RecordUsageBatch: %v", err)
	}

	assertTableRowCount(t, sqliteStore, "usage_log", len(records))
	updatedKey, err := sqliteStore.GetKeyByID(ctx, apiKey.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedKey.TotalCalls != int64(len(records)) {
		t.Fatalf("total calls = %d, want %d", updatedKey.TotalCalls, len(records))
	}
	if updatedKey.LastUsedAt == nil || !updatedKey.LastUsedAt.Equal(latestTimestamp) {
		t.Fatalf("last used at = %v, want %v", updatedKey.LastUsedAt, latestTimestamp)
	}

	metrics := sqliteStore.SQLiteMetrics()
	if metrics.UsageWrite.Operation.Attempts != 1 || metrics.UsageWrite.RecordsSucceeded != uint64(len(records)) {
		t.Fatalf("unexpected usage write metrics: %+v", metrics.UsageWrite)
	}
}

func TestRecordUsageBatchRollsBackPrimaryTransactionOnInvalidRecord(t *testing.T) {
	sqliteStore := openTestDB(t)
	sqliteStore.SetMetricsEnabled(true)
	ctx := context.Background()
	userID := testUserID(t, sqliteStore)
	apiKey, _, err := sqliteStore.CreateKey(ctx, userID, "rollback-batch-key", 20)
	if err != nil {
		t.Fatal(err)
	}

	err = sqliteStore.RecordUsageBatch(ctx, []UsageRecord{
		{KeyID: apiKey.ID, ToolName: "grok_web_search", Timestamp: time.Now().UTC(), Success: true},
		{KeyID: "missing-api-key", ToolName: "grok_x_search", Timestamp: time.Now().UTC(), Success: false},
	})
	if err == nil {
		t.Fatal("expected invalid batch record to fail")
	}

	assertTableRowCount(t, sqliteStore, "usage_log", 0)
	updatedKey, getErr := sqliteStore.GetKeyByID(ctx, apiKey.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if updatedKey.TotalCalls != 0 || updatedKey.LastUsedAt != nil {
		t.Fatalf("API key usage changed after rollback: %+v", updatedKey)
	}
	metrics := sqliteStore.SQLiteMetrics()
	if metrics.UsageWrite.RecordsFailed != 2 || metrics.UsageWrite.Operation.Errors != 1 {
		t.Fatalf("unexpected failed usage metrics: %+v", metrics.UsageWrite)
	}
}

func TestRecordUsageBatchPersistsDebugRecordsWithoutRunningCleanup(t *testing.T) {
	sqliteStore := openTestDB(t)
	ctx := context.Background()
	userID := testUserID(t, sqliteStore)
	apiKey, _, err := sqliteStore.CreateKey(ctx, userID, "debug-batch-key", 20)
	if err != nil {
		t.Fatal(err)
	}

	var cleanupCount atomic.Int64
	records := make([]UsageRecord, 0, 2)
	for recordIndex := 0; recordIndex < 2; recordIndex++ {
		requestFile, createErr := os.CreateTemp(t.TempDir(), "batch-request-*.body")
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := requestFile.WriteString("request body"); writeErr != nil {
			t.Fatal(writeErr)
		}
		if closeErr := requestFile.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		records = append(records, UsageRecord{
			KeyID:                apiKey.ID,
			ToolName:             "grok_web_search",
			Timestamp:            time.Now().UTC(),
			Success:              true,
			DebugJSON:            `{"batch":true}`,
			DebugRequestBodyPath: requestFile.Name(),
			Cleanup: func() {
				cleanupCount.Add(1)
			},
		})
	}

	if err := sqliteStore.RecordUsageBatch(ctx, records); err != nil {
		t.Fatalf("RecordUsageBatch: %v", err)
	}
	if cleanupCount.Load() != 0 {
		t.Fatalf("store invoked writer-owned cleanup %d times", cleanupCount.Load())
	}
	assertDebugTableRowCount(t, sqliteStore, 2)
}

func assertDebugTableRowCount(t *testing.T, sqliteStore *SQLiteStore, expectedCount int) {
	t.Helper()
	var actualCount int
	if err := sqliteStore.debugDB.QueryRow(`SELECT COUNT(*) FROM usage_debug`).Scan(&actualCount); err != nil {
		t.Fatal(err)
	}
	if actualCount != expectedCount {
		t.Fatalf("usage_debug row count = %d, want %d", actualCount, expectedCount)
	}
}
