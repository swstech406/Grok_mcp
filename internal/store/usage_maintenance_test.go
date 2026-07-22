package store

import (
	"context"
	"testing"
	"time"
)

func TestUsageRollupMigrationCreatesHistoryTables(t *testing.T) {
	sqliteStore := openTestDB(t)

	for _, tableName := range []string{"usage_hourly_rollups", "usage_daily_rollups"} {
		var tableCount int
		if err := sqliteStore.db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
			tableName,
		).Scan(&tableCount); err != nil {
			t.Fatalf("query table %s: %v", tableName, err)
		}
		if tableCount != 1 {
			t.Fatalf("table %s count = %d, want 1", tableName, tableCount)
		}
	}
}

func TestUsageMaintenanceCompactsRetainsAndPreservesStatistics(t *testing.T) {
	sqliteStore := openTestDB(t)
	sqliteStore.SetMetricsEnabled(true)
	ctx := context.Background()
	userID := testUserID(t, sqliteStore)
	apiKey, _, err := sqliteStore.CreateKey(ctx, userID, "maintenance-key", 20)
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	records := []UsageRecord{
		{
			KeyID: apiKey.ID, ToolName: "web_search", Timestamp: now.Add(-2 * time.Hour),
			DurationMs: 10, Success: true, DebugJSON: `{"tier":"raw"}`,
		},
		{
			KeyID: apiKey.ID, ToolName: "x_search", Timestamp: now.Add(-30 * time.Hour),
			DurationMs: 20, Success: false, DebugJSON: `{"tier":"hourly"}`,
		},
		{
			KeyID: apiKey.ID, ToolName: "web_search", Timestamp: now.Add(-100 * time.Hour),
			DurationMs: 30, Success: true, DebugJSON: `{"tier":"daily"}`,
		},
		{
			KeyID: apiKey.ID, ToolName: "list_models", Timestamp: now.Add(-300 * time.Hour),
			DurationMs: 40, Success: true, DebugJSON: `{"tier":"expired"}`,
		},
	}
	for _, record := range records {
		if err := sqliteStore.RecordUsage(ctx, record); err != nil {
			t.Fatalf("RecordUsage(%s): %v", record.ToolName, err)
		}
	}

	policy := UsageRetentionPolicy{
		RawRetention:    24 * time.Hour,
		HourlyRetention: 72 * time.Hour,
		DailyRetention:  240 * time.Hour,
	}
	result, err := sqliteStore.RunUsageMaintenance(ctx, policy, now)
	if err != nil {
		t.Fatalf("RunUsageMaintenance: %v", err)
	}
	if result.RawRowsCompacted != 3 {
		t.Fatalf("raw rows compacted = %d, want 3", result.RawRowsCompacted)
	}
	if result.HourlyRowsCompacted != 2 {
		t.Fatalf("hourly rows compacted = %d, want 2", result.HourlyRowsCompacted)
	}
	if result.DailyRowsDeleted != 1 {
		t.Fatalf("daily rows deleted = %d, want 1", result.DailyRowsDeleted)
	}
	if result.DebugRowsDeleted != 3 {
		t.Fatalf("debug rows deleted = %d, want 3", result.DebugRowsDeleted)
	}
	maintenanceMetrics := sqliteStore.SQLiteMetrics()
	if maintenanceMetrics.UsageMaintenance.Attempts != 1 {
		t.Fatalf("maintenance attempts = %d, want 1", maintenanceMetrics.UsageMaintenance.Attempts)
	}
	if maintenanceMetrics.PrimaryWALCheckpoint.Operation.Attempts != 1 ||
		maintenanceMetrics.DebugWALCheckpoint.Operation.Attempts != 1 {
		t.Fatalf("unexpected checkpoint metrics: primary=%+v debug=%+v",
			maintenanceMetrics.PrimaryWALCheckpoint,
			maintenanceMetrics.DebugWALCheckpoint,
		)
	}

	assertTableRowCount(t, sqliteStore, "usage_log", 1)
	assertTableRowCount(t, sqliteStore, "usage_hourly_rollups", 1)
	assertTableRowCount(t, sqliteStore, "usage_daily_rollups", 1)

	stats, err := sqliteStore.GetUsageStats(ctx, apiKey.ID, now.Add(-400*time.Hour))
	if err != nil {
		t.Fatalf("GetUsageStats: %v", err)
	}
	if stats.TotalCalls != 3 || stats.SuccessCalls != 2 {
		t.Fatalf("usage totals = (%d, %d), want (3, 2)", stats.TotalCalls, stats.SuccessCalls)
	}
	if stats.ByTool["web_search"] != 2 || stats.ByTool["x_search"] != 1 {
		t.Fatalf("unexpected tool totals: %+v", stats.ByTool)
	}
	if stats.ByTool["list_models"] != 0 {
		t.Fatalf("expired tool usage remained in statistics: %+v", stats.ByTool)
	}
	if len(stats.Records) != 1 || stats.Records[0].ToolName != "web_search" {
		t.Fatalf("raw records = %+v, want only the recent web_search call", stats.Records)
	}
	var trafficCalls int64
	for _, bucket := range stats.TrafficBuckets {
		trafficCalls += bucket.Calls
	}
	if trafficCalls != 3 {
		t.Fatalf("traffic bucket calls = %d, want 3", trafficCalls)
	}

	userStats, err := sqliteStore.GetUserUsageStats(ctx, userID, now.Add(-400*time.Hour))
	if err != nil {
		t.Fatalf("GetUserUsageStats: %v", err)
	}
	if userStats.TotalCalls != 3 || userStats.SuccessCalls != 2 {
		t.Fatalf("user usage totals = (%d, %d), want (3, 2)", userStats.TotalCalls, userStats.SuccessCalls)
	}
	globalStats, err := sqliteStore.GetGlobalStats(ctx, now.Add(-400*time.Hour))
	if err != nil {
		t.Fatalf("GetGlobalStats: %v", err)
	}
	if globalStats.TotalCalls != 3 || globalStats.SuccessCalls != 2 {
		t.Fatalf("global usage totals = (%d, %d), want (3, 2)", globalStats.TotalCalls, globalStats.SuccessCalls)
	}

	secondResult, err := sqliteStore.RunUsageMaintenance(ctx, policy, now)
	if err != nil {
		t.Fatalf("second RunUsageMaintenance: %v", err)
	}
	if secondResult.RawRowsCompacted != 0 || secondResult.HourlyRowsCompacted != 0 || secondResult.DailyRowsDeleted != 0 {
		t.Fatalf("second maintenance pass was not idempotent: %+v", secondResult)
	}

	statsAfterSecondPass, err := sqliteStore.GetUsageStats(ctx, apiKey.ID, now.Add(-400*time.Hour))
	if err != nil {
		t.Fatalf("GetUsageStats after second pass: %v", err)
	}
	if statsAfterSecondPass.TotalCalls != stats.TotalCalls || statsAfterSecondPass.SuccessCalls != stats.SuccessCalls {
		t.Fatalf("statistics changed after idempotent pass: before=%+v after=%+v", stats, statsAfterSecondPass)
	}
}

func TestUsageMaintenanceKeepsRowsAtRawCutoff(t *testing.T) {
	sqliteStore := openTestDB(t)
	ctx := context.Background()
	userID := testUserID(t, sqliteStore)
	apiKey, _, err := sqliteStore.CreateKey(ctx, userID, "boundary-key", 20)
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Hour).Add(30 * time.Minute)
	rawCutoff := now.Add(-24 * time.Hour).Truncate(time.Hour)
	if err := sqliteStore.RecordUsage(ctx, UsageRecord{
		KeyID: apiKey.ID, ToolName: "web_search", Timestamp: rawCutoff,
		DurationMs: 10, Success: true,
	}); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}

	_, err = sqliteStore.RunUsageMaintenance(ctx, UsageRetentionPolicy{
		RawRetention:    24 * time.Hour,
		HourlyRetention: 72 * time.Hour,
		DailyRetention:  240 * time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("RunUsageMaintenance: %v", err)
	}

	assertTableRowCount(t, sqliteStore, "usage_log", 1)
	assertTableRowCount(t, sqliteStore, "usage_hourly_rollups", 0)
}

func assertTableRowCount(t *testing.T, sqliteStore *SQLiteStore, tableName string, expectedCount int) {
	t.Helper()
	var actualCount int
	if err := sqliteStore.db.QueryRow(`SELECT COUNT(*) FROM ` + tableName).Scan(&actualCount); err != nil {
		t.Fatalf("count %s rows: %v", tableName, err)
	}
	if actualCount != expectedCount {
		t.Fatalf("%s row count = %d, want %d", tableName, actualCount, expectedCount)
	}
}
