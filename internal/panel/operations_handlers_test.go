package panel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/config"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/ratelimit"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

type recordingSQLiteMetricsProvider struct {
	callCount int
}

func (provider *recordingSQLiteMetricsProvider) SQLiteMetrics() store.SQLiteMetricsSnapshot {
	provider.callCount++
	return store.SQLiteMetricsSnapshot{}
}

type recordingUsageWriterMetricsProvider struct {
	callCount int
}

func (provider *recordingUsageWriterMetricsProvider) Stats() store.AsyncUsageWriterStats {
	provider.callCount++
	return store.AsyncUsageWriterStats{}
}

type recordingIPLimiterMetricsProvider struct {
	callCount int
	snapshot  ratelimit.IPLimiterMetricsSnapshot
}

func (provider *recordingIPLimiterMetricsProvider) Metrics() ratelimit.IPLimiterMetricsSnapshot {
	provider.callCount++
	return provider.snapshot
}

func TestAdminOperationalMetricsReturnsSQLiteAndWriterSnapshots(t *testing.T) {
	sqliteStore, err := store.OpenSQLite(filepath.Join(t.TempDir(), "operations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()
	const jwtSecret = "operations-jwt-secret-must-be-at-least-32-bytes"
	if err := sqliteStore.ConfigureAPIKeyEncryption(jwtSecret); err != nil {
		t.Fatal(err)
	}
	usageWriter := store.NewAsyncUsageWriter(sqliteStore, 17)
	usageWriter.SetMetricsEnabled(true)
	defer usageWriter.Close()
	sqliteStore.SetMetricsEnabled(true)
	expectedIPLimiterMetrics := ratelimit.IPLimiterMetricsSnapshot{
		CurrentEntries:        12,
		MaximumEntries:        131072,
		FallbackBucketCount:   1024,
		DedicatedAdmissions:   15,
		ExpiredEntriesRemoved: 3,
		FallbackRequests:      7,
		FallbackRejections:    2,
	}
	ipLimiterMetrics := &recordingIPLimiterMetricsProvider{snapshot: expectedIPLimiterMetrics}
	authProtector := NewAuthProtector(AuthProtectorConfig{
		LoginIPMaximumEntries:                 1,
		RegisterIPMaximumEntries:              2,
		RegistrationChallengeIPMaximumEntries: 3,
		LoginFailureMaximumEntries:            4,
		AuthEndpointFallbackBuckets:           1,
	})
	authProtector.allowAuthRequest(authEndpointLogin, "198.51.100.50")
	authProtector.allowAuthRequest(authEndpointLogin, "198.51.100.51")
	userLimiter := ratelimit.NewUserLimiter()
	defer userLimiter.Close()
	loginAttempt, _ := authProtector.beginLoginAttempt("metrics-user", "198.51.100.50")
	if loginAttempt == nil {
		t.Fatal("metrics login attempt should be admitted")
	}
	loginAttempt.recordFailure()
	_, err = sqliteStore.UpsertServerSettings(context.Background(), store.ServerSettings{Runtime: config.ServerSettings{
		CPABaseURL:                 "https://cpa.example.test",
		CPAAPIKey:                  "operations-api-key",
		UpstreamProtocol:           config.UpstreamProtocolResponses,
		Model:                      "grok-4.3",
		TimeoutSeconds:             120,
		MCPGlobalSearchConcurrency: 16,
		MCPUserSearchConcurrency:   4,
		RegistrationMode:           store.RegistrationModeFree,
		OperationsMetricsEnabled:   true,
	}})
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(NewMux(&Handler{
		Store:              sqliteStore,
		JWTSecret:          jwtSecret,
		SQLiteMetrics:      sqliteStore,
		UsageWriterMetrics: usageWriter,
		IPLimiterMetrics:   ipLimiterMetrics,
		UserLimiterMetrics: userLimiter,
		AuthProtector:      authProtector,
	}))
	defer server.Close()

	createPanelAdminUser(t, sqliteStore, "operations-admin", "password123")
	loginResponse := loginPanelUser(t, server, "operations-admin", "password123")
	expectedAuthProtectorMetrics := authProtector.Metrics()
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/panel/v1/admin/operations/metrics", nil)
	request = withJWT(request, loginResponse.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", response.StatusCode)
	}

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}

	var payload operationalMetricsResponse
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		t.Fatal(err)
	}
	assertIPLimiterMetricsJSONFields(t, responseBody)
	assertUserLimiterMetricsJSONFields(t, responseBody)
	assertAuthProtectorMetricsJSONFields(t, responseBody)
	if payload.SQLite.PrimaryWritePool.MaximumOpenConnections != 1 {
		t.Fatalf("primary write pool metrics = %+v", payload.SQLite.PrimaryWritePool)
	}
	if payload.UsageWriter.QueueCapacity != 17 {
		t.Fatalf("usage writer queue capacity = %d, want 17", payload.UsageWriter.QueueCapacity)
	}
	if payload.IPLimiter != expectedIPLimiterMetrics {
		t.Fatalf("IP limiter metrics = %+v, want %+v", payload.IPLimiter, expectedIPLimiterMetrics)
	}
	if payload.AuthProtector != expectedAuthProtectorMetrics {
		t.Fatalf("auth protector metrics = %+v, want %+v", payload.AuthProtector, expectedAuthProtectorMetrics)
	}
	if ipLimiterMetrics.callCount != 1 {
		t.Fatalf("IP limiter metrics call count = %d, want 1", ipLimiterMetrics.callCount)
	}
}

func assertAuthProtectorMetricsJSONFields(t *testing.T, responseBody []byte) {
	t.Helper()

	var rawPayload map[string]json.RawMessage
	if err := json.Unmarshal(responseBody, &rawPayload); err != nil {
		t.Fatal(err)
	}
	var rawAuthProtectorMetrics map[string]json.RawMessage
	if err := json.Unmarshal(rawPayload["auth_protector"], &rawAuthProtectorMetrics); err != nil {
		t.Fatalf("decode raw auth_protector metrics: %v", err)
	}
	expectedGroupNames := []string{"login", "register", "registration_challenge", "login_failures", "username_failures", "password_work"}
	for _, expectedGroupName := range expectedGroupNames {
		if _, exists := rawAuthProtectorMetrics[expectedGroupName]; !exists {
			t.Fatalf("auth_protector group %q is missing from raw JSON: %s", expectedGroupName, string(responseBody))
		}
	}
	if len(rawAuthProtectorMetrics) != len(expectedGroupNames) {
		t.Fatalf("unexpected auth_protector metric groups in raw JSON: %+v", rawAuthProtectorMetrics)
	}

	assertRawMetricFields(t, rawAuthProtectorMetrics["login"], []string{
		"current_entries",
		"maximum_entries",
		"fallback_bucket_count",
		"dedicated_admissions",
		"expired_entries_removed",
		"fallback_requests",
		"fallback_rejections",
	})
	assertRawMetricFields(t, rawAuthProtectorMetrics["register"], []string{
		"current_entries",
		"maximum_entries",
		"fallback_bucket_count",
		"dedicated_admissions",
		"expired_entries_removed",
		"fallback_requests",
		"fallback_rejections",
	})
	assertRawMetricFields(t, rawAuthProtectorMetrics["registration_challenge"], []string{
		"current_entries",
		"maximum_entries",
		"fallback_bucket_count",
		"dedicated_admissions",
		"expired_entries_removed",
		"fallback_requests",
		"fallback_rejections",
	})
	assertRawMetricFields(t, rawAuthProtectorMetrics["login_failures"], []string{
		"current_entries",
		"maximum_entries",
		"fallback_bucket_count",
		"admissions",
		"expired_entries_removed",
		"capacity_rejections",
		"fallback_attempts",
		"fallback_rejections",
	})
	assertRawMetricFields(t, rawAuthProtectorMetrics["username_failures"], []string{
		"current_entries",
		"maximum_entries",
		"fallback_bucket_count",
		"admissions",
		"expired_entries_removed",
		"capacity_rejections",
		"fallback_attempts",
		"fallback_rejections",
	})
	assertRawMetricFields(t, rawAuthProtectorMetrics["password_work"], []string{
		"current_work",
		"capacity",
		"admissions",
		"rejections",
	})
}

func assertRawMetricFields(t *testing.T, rawMetrics json.RawMessage, expectedFieldNames []string) {
	t.Helper()

	var metricFields map[string]json.RawMessage
	if err := json.Unmarshal(rawMetrics, &metricFields); err != nil {
		t.Fatal(err)
	}
	for _, expectedFieldName := range expectedFieldNames {
		if _, exists := metricFields[expectedFieldName]; !exists {
			t.Fatalf("metric field %q is missing from raw JSON: %s", expectedFieldName, string(rawMetrics))
		}
		delete(metricFields, expectedFieldName)
	}
	if len(metricFields) != 0 {
		t.Fatalf("unexpected metric fields in raw JSON: %+v", metricFields)
	}
}

func assertIPLimiterMetricsJSONFields(t *testing.T, responseBody []byte) {
	t.Helper()

	var rawPayload map[string]json.RawMessage
	if err := json.Unmarshal(responseBody, &rawPayload); err != nil {
		t.Fatal(err)
	}
	var rawIPLimiterMetrics map[string]json.RawMessage
	if err := json.Unmarshal(rawPayload["ip_limiter"], &rawIPLimiterMetrics); err != nil {
		t.Fatalf("decode raw ip_limiter metrics: %v", err)
	}
	expectedFieldNames := []string{
		"current_entries",
		"maximum_entries",
		"fallback_bucket_count",
		"dedicated_admissions",
		"expired_entries_removed",
		"fallback_requests",
		"fallback_rejections",
	}
	for _, expectedFieldName := range expectedFieldNames {
		if _, exists := rawIPLimiterMetrics[expectedFieldName]; !exists {
			t.Fatalf("ip_limiter metric %q is missing from raw JSON: %s", expectedFieldName, string(responseBody))
		}
		delete(rawIPLimiterMetrics, expectedFieldName)
	}
	if len(rawIPLimiterMetrics) != 0 {
		t.Fatalf("unexpected ip_limiter metric fields in raw JSON: %+v", rawIPLimiterMetrics)
	}
}

func assertUserLimiterMetricsJSONFields(t *testing.T, responseBody []byte) {
	t.Helper()

	var rawPayload map[string]json.RawMessage
	if err := json.Unmarshal(responseBody, &rawPayload); err != nil {
		t.Fatal(err)
	}
	assertRawMetricFields(t, rawPayload["user_limiter"], []string{
		"current_entries",
		"maximum_entries",
		"fallback_bucket_count",
		"dedicated_admissions",
		"expired_entries_removed",
		"fallback_requests",
		"fallback_rejections",
	})
}

func TestAdminOperationalMetricsAreDisabledByDefault(t *testing.T) {
	sqliteStore, err := store.OpenSQLite(filepath.Join(t.TempDir(), "operations-disabled.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	sqliteProvider := &recordingSQLiteMetricsProvider{}
	usageWriterProvider := &recordingUsageWriterMetricsProvider{}
	ipLimiterProvider := &recordingIPLimiterMetricsProvider{}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/panel/v1/admin/operations/metrics", nil)
	(&Handler{
		Store:              sqliteStore,
		SQLiteMetrics:      sqliteProvider,
		UsageWriterMetrics: usageWriterProvider,
		IPLimiterMetrics:   ipLimiterProvider,
	}).adminOperationalMetrics(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("metrics status = %d, want 404", recorder.Code)
	}
	if sqliteProvider.callCount != 0 || usageWriterProvider.callCount != 0 || ipLimiterProvider.callCount != 0 {
		t.Fatalf(
			"disabled endpoint read metric snapshots: sqlite=%d writer=%d ip_limiter=%d",
			sqliteProvider.callCount,
			usageWriterProvider.callCount,
			ipLimiterProvider.callCount,
		)
	}
}

func TestAdminOperationalMetricsRejectsRegularUsers(t *testing.T) {
	testServer, sqliteStore, _ := panelTestServer(t)
	defer testServer.Close()

	createPanelAdminUser(t, sqliteStore, "first-admin", "password123")
	registerPanelUser(t, testServer, "regular-operations-user", "password123")
	loginResponse := loginPanelUser(t, testServer, "regular-operations-user", "password123")
	request, _ := http.NewRequest(http.MethodGet, testServer.URL+"/panel/v1/admin/operations/metrics", nil)
	request = withJWT(request, loginResponse.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("regular-user metrics status = %d, want 403", response.StatusCode)
	}
}

func TestAdminOperationalMetricsReportsUnavailableProviders(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/panel/v1/admin/operations/metrics", nil)
	(&Handler{}).adminOperationalMetrics(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("metrics status = %d, want 503", recorder.Code)
	}
}
