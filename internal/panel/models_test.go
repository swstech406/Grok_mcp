package panel

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

func TestToUserResponseWithTierMarksUnavailableWhenTierMissing(t *testing.T) {
	user := &store.User{ID: "u1", Username: "alice", Role: store.RoleUser, Enabled: true, TierID: "gone", SuccessCalls: 3}
	resp := toUserResponseWithTier(user, nil)
	if !resp.LimitsUnavailable {
		t.Fatal("expected limits_unavailable when tier is nil")
	}
	if resp.RPM != 0 || resp.SuccessLimit != 0 {
		t.Fatalf("rpm/success must stay 0 when unavailable, got rpm=%d success=%d", resp.RPM, resp.SuccessLimit)
	}
	if resp.SuccessCalls != 3 {
		t.Fatalf("success_calls should still surface, got %d", resp.SuccessCalls)
	}
}

func TestToUserResponseWithTierUsesTierLimits(t *testing.T) {
	user := &store.User{ID: "u1", Username: "alice", Role: store.RoleUser, Enabled: true, TierID: "t1"}
	tier := &store.Tier{ID: "t1", Name: "custom", Level: 9, RPM: 12, SuccessLimit: 34}
	resp := toUserResponseWithTier(user, tier)
	if resp.LimitsUnavailable {
		t.Fatal("limits must be available when tier is present")
	}
	if resp.RPM != 12 || resp.SuccessLimit != 34 || resp.TierName != "custom" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestToUsageStatsResponseIncludesDatabaseAggregates(t *testing.T) {
	bucketStart := time.Date(2026, time.July, 10, 8, 0, 0, 0, time.UTC)
	bucketEnd := bucketStart.Add(3 * time.Hour)
	response := toUsageStatsResponse(&store.UsageStats{
		TotalCalls:   650,
		SuccessCalls: 640,
		CurrentRPM:   612,
		ByTool:       map[string]int64{"grok_web_search": 650},
		TrafficBuckets: []store.UsageBucket{
			{Start: bucketStart, End: bucketEnd, Calls: 650},
		},
	})

	if response.CurrentRPM != 612 {
		t.Fatalf("expected current RPM in response, got %d", response.CurrentRPM)
	}
	if len(response.TrafficBuckets) != 1 {
		t.Fatalf("expected one traffic bucket, got %d", len(response.TrafficBuckets))
	}
	bucket := response.TrafficBuckets[0]
	if !bucket.Start.Equal(bucketStart) || !bucket.End.Equal(bucketEnd) || bucket.Calls != 650 {
		t.Fatalf("unexpected traffic bucket response: %+v", bucket)
	}
}

func TestToUsageStatsResponseOmitsCompleteDebugBodies(t *testing.T) {
	const requestBody = "unique-sensitive-request-body"
	const responseBody = "unique-sensitive-response-body"
	usageRecord := store.UsageRecord{
		ID: 17, KeyID: "key-1", ToolName: "grok_web_search", Timestamp: time.Now().UTC(),
		DebugJSON: `{"version":2}`, HasDebugRequestBody: true, HasDebugResponseBody: true,
		DebugRequestBytes: int64(len(requestBody)), DebugResponseBytes: int64(len(responseBody)),
		DebugRequestBody: requestBody, DebugResponseBody: responseBody,
	}

	responseJSON, err := json.Marshal(toUsageStatsResponse(&store.UsageStats{Records: []store.UsageRecord{usageRecord}}))
	if err != nil {
		t.Fatal(err)
	}
	serializedResponse := string(responseJSON)
	for _, forbiddenValue := range []string{"\"debug_request_body\":", "\"debug_response_body\":", requestBody, responseBody} {
		if strings.Contains(serializedResponse, forbiddenValue) {
			t.Fatalf("usage list exposed complete debug body data: %s", serializedResponse)
		}
	}
	if !strings.Contains(serializedResponse, "has_debug_request_body") || !strings.Contains(serializedResponse, "debug_request_body_bytes") {
		t.Fatalf("usage list omitted safe debug body summary: %s", serializedResponse)
	}

	detailJSON, err := json.Marshal(toUsageRecordDetailResponse(&usageRecord))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(detailJSON), requestBody) || !strings.Contains(string(detailJSON), responseBody) {
		t.Fatalf("usage detail omitted complete debug bodies: %s", detailJSON)
	}
}
