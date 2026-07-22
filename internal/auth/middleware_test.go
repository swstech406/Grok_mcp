package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/keyhash"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

type memStore struct {
	store.TestStore
	byHash map[string]*store.APIKey
	users  map[string]*store.User
}

func (m *memStore) GetKeyByHash(_ context.Context, hash string) (*store.APIKey, error) {
	return m.byHash[hash], nil
}

func (m *memStore) GetUserByID(_ context.Context, id string) (*store.User, error) {
	if u, ok := m.users[id]; ok {
		return u, nil
	}
	return nil, store.ErrUserNotFound
}

func (m *memStore) GetTierByID(_ context.Context, id string) (*store.Tier, error) {
	if id == "tier0-id" {
		return &store.Tier{ID: "tier0-id", Name: "tier0", RPM: 10, SuccessLimit: 800}, nil
	}
	return nil, store.ErrTierNotFound
}

func TestAPIKeyMiddleware(t *testing.T) {
	raw := generatedAPIKeyForTest('a')
	hash := keyhash.HashAPIKey(raw)
	st := &memStore{
		byHash: map[string]*store.APIKey{
			hash: {ID: "id-1", UserID: "u1", Enabled: true},
		},
		users: map[string]*store.User{
			"u1": {ID: "u1", Enabled: true, TierID: "tier0-id"},
		},
	}

	var gotID string
	h := APIKeyMiddleware(NewStoreAPIKeyResolver(st))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k, ok := APIKeyFromContext(r.Context())
		if !ok {
			t.Fatal("missing key in context")
		}
		gotID = k.ID
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotID != "id-1" {
		t.Fatalf("code=%d id=%s", rec.Code, gotID)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec2.Code)
	}
}

func TestAPIKeyMiddlewareRejectsInvalidDisabledKeyAndDisabledUser(t *testing.T) {
	testCases := []struct {
		name     string
		key      *store.APIKey
		user     *store.User
		want     int
		wantBody string
	}{
		{name: "unknown key", want: http.StatusForbidden, wantBody: "invalid API key"},
		{
			name:     "disabled key",
			key:      &store.APIKey{ID: "k-disabled", UserID: "u1", Enabled: false},
			user:     &store.User{ID: "u1", Enabled: true, TierID: "tier0-id"},
			want:     http.StatusForbidden,
			wantBody: "API key disabled",
		},
		{
			name:     "disabled user",
			key:      &store.APIKey{ID: "k-user-disabled", UserID: "u1", Enabled: true},
			user:     &store.User{ID: "u1", Enabled: false, TierID: "tier0-id"},
			want:     http.StatusForbidden,
			wantBody: "user disabled",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			raw := generatedAPIKeyForTest('b')
			hash := keyhash.HashAPIKey(raw)
			st := &memStore{byHash: map[string]*store.APIKey{}, users: map[string]*store.User{}}
			if testCase.key != nil {
				st.byHash[hash] = testCase.key
			}
			if testCase.user != nil {
				st.users[testCase.user.ID] = testCase.user
			}

			handler := APIKeyMiddleware(NewStoreAPIKeyResolver(st))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", "Bearer "+raw)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != testCase.want {
				t.Fatalf("status = %d, want %d", rec.Code, testCase.want)
			}
			if !strings.Contains(rec.Body.String(), testCase.wantBody) {
				t.Fatalf("response body = %q, want it to contain %q", rec.Body.String(), testCase.wantBody)
			}
		})
	}
}

func TestAPIKeyMiddlewareResolverErrorReturnsInternalServerError(t *testing.T) {
	handler := APIKeyMiddleware(failingAPIKeyResolver{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+generatedAPIKeyForTest('c'))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestAPIKeyMiddlewareRejectsMalformedGeneratedKeysBeforeResolver(t *testing.T) {
	testCases := []string{
		"grok_short",
		"grok_" + strings.Repeat("A", 64),
		"grok_" + strings.Repeat("g", 64),
		"other_" + strings.Repeat("a", 64),
	}
	for _, malformedKey := range testCases {
		resolver := &recordingAPIKeyResolver{}
		handler := APIKeyMiddleware(resolver)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("malformed API key reached next handler")
		}))
		request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		request.Header.Set("Authorization", "Bearer "+malformedKey)
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		if response.Code != http.StatusForbidden {
			t.Fatalf("key %q status = %d, want %d", malformedKey, response.Code, http.StatusForbidden)
		}
		if resolver.callCount != 0 {
			t.Fatalf("key %q caused %d resolver calls, want 0", malformedKey, resolver.callCount)
		}
	}
}

func TestAuthorizationHeaderRejectionsDoNotReachAuthenticationDependencies(t *testing.T) {
	panelUser := &store.User{
		ID:           "authorization-header-user",
		Username:     "authorization-header-user",
		Role:         store.RoleUser,
		Enabled:      true,
		TokenVersion: 0,
	}
	panelToken, _, err := IssuePanelToken(testSecret, panelUser, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	apiKey := generatedAPIKeyForTest('f')

	testCases := []struct {
		name         string
		headerValues func(string) []string
	}{
		{name: "missing", headerValues: func(string) []string { return nil }},
		{name: "duplicate", headerValues: func(token string) []string { return []string{"Bearer " + token, "Bearer " + token} }},
		{name: "wrong scheme", headerValues: func(token string) []string { return []string{"Basic " + token} }},
		{name: "empty credential", headerValues: func(string) []string { return []string{"Bearer "} }},
		{name: "leading whitespace", headerValues: func(token string) []string { return []string{" Bearer " + token} }},
		{name: "trailing whitespace", headerValues: func(token string) []string { return []string{"Bearer " + token + " "} }},
		{name: "multiple spaces", headerValues: func(token string) []string { return []string{"Bearer  " + token} }},
		{name: "tab separator", headerValues: func(token string) []string { return []string{"Bearer\t" + token} }},
		{name: "credential whitespace", headerValues: func(token string) []string { return []string{"Bearer " + token + " extra"} }},
		{name: "comma joined credentials", headerValues: func(token string) []string { return []string{"Bearer " + token + ",Bearer " + token} }},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Run("panel JWT", func(t *testing.T) {
				loader := &recordingPanelTokenLoader{}
				downstreamCallCount := 0
				handler := JWTMiddleware(testSecret, loader)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
					downstreamCallCount++
				}))
				request := httptest.NewRequest(http.MethodGet, "/panel/v1/me", nil)
				for _, headerValue := range testCase.headerValues(panelToken) {
					request.Header.Add("Authorization", headerValue)
				}
				responseRecorder := httptest.NewRecorder()

				handler.ServeHTTP(responseRecorder, request)

				if responseRecorder.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want %d", responseRecorder.Code, http.StatusUnauthorized)
				}
				if loader.userLoadCount != 0 || loader.tierLoadCount != 0 {
					t.Fatalf("malformed header caused panel store loads: %+v", loader)
				}
				if downstreamCallCount != 0 {
					t.Fatalf("malformed header reached panel downstream %d time(s)", downstreamCallCount)
				}
			})

			t.Run("MCP API key", func(t *testing.T) {
				resolver := &recordingAPIKeyResolver{}
				downstreamCallCount := 0
				handler := APIKeyMiddleware(resolver)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
					downstreamCallCount++
				}))
				request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
				for _, headerValue := range testCase.headerValues(apiKey) {
					request.Header.Add("Authorization", headerValue)
				}
				responseRecorder := httptest.NewRecorder()

				handler.ServeHTTP(responseRecorder, request)

				if responseRecorder.Code != http.StatusUnauthorized && responseRecorder.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want authentication rejection", responseRecorder.Code)
				}
				if resolver.callCount != 0 {
					t.Fatalf("malformed header caused %d MCP resolver calls", resolver.callCount)
				}
				if downstreamCallCount != 0 {
					t.Fatalf("malformed header reached MCP downstream %d time(s)", downstreamCallCount)
				}
			})
		})
	}
}

func TestAPIKeyMiddlewareMapsResolverSaturationToServiceUnavailable(t *testing.T) {
	handler := APIKeyMiddleware(saturatedAPIKeyResolver{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("saturated resolver reached next handler")
	}))
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.Header.Set("Authorization", "Bearer "+generatedAPIKeyForTest('e'))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if response.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q, want 1", response.Header().Get("Retry-After"))
	}
}

type recordingAPIKeyResolver struct {
	callCount int
}

func (resolver *recordingAPIKeyResolver) Resolve(context.Context, string) (*store.APIKey, *AuthenticatedUser, error) {
	resolver.callCount++
	return nil, nil, nil
}

type saturatedAPIKeyResolver struct{}

func (saturatedAPIKeyResolver) Resolve(context.Context, string) (*store.APIKey, *AuthenticatedUser, error) {
	return nil, nil, ErrAPIKeyResolverSaturated
}

type failingAPIKeyResolver struct{}

func (failingAPIKeyResolver) Resolve(context.Context, string) (*store.APIKey, *AuthenticatedUser, error) {
	return nil, nil, errors.New("resolver unavailable")
}

func TestCachedAPIKeyResolverReturnsClonesAndInvalidates(t *testing.T) {
	keyHash := "hash-for-cache"
	st := &cacheResolverStore{
		key:  &store.APIKey{ID: "k1", UserID: "u1", Name: "original", Enabled: true},
		user: &store.User{ID: "u1", Enabled: true, TierID: "tier-paid"},
		tier: &store.Tier{ID: "tier-paid", RPM: 42, SuccessLimit: 84},
	}
	resolver := NewCachedAPIKeyResolver(st, time.Hour)
	t.Cleanup(resolver.Close)

	firstKey, firstUser, err := resolver.Resolve(context.Background(), keyHash)
	if err != nil {
		t.Fatal(err)
	}
	firstKey.Enabled = false
	firstUser.Enabled = false
	firstUser.RPM = 999

	secondKey, secondUser, err := resolver.Resolve(context.Background(), keyHash)
	if err != nil {
		t.Fatal(err)
	}
	if st.keyLookups != 1 || st.userLookups != 1 {
		t.Fatalf("expected second resolve to use the cached authentication snapshot, keyLookups=%d userLookups=%d", st.keyLookups, st.userLookups)
	}
	if !secondKey.Enabled || !secondUser.Enabled || secondUser.RPM != 42 || secondUser.SuccessLimit != 84 {
		t.Fatalf("cached values must be cloned and tier-enriched, key=%+v user=%+v", secondKey, secondUser)
	}

	st.key.Name = "after-invalidate"
	resolver.InvalidateAll()
	thirdKey, _, err := resolver.Resolve(context.Background(), keyHash)
	if err != nil {
		t.Fatal(err)
	}
	if thirdKey.Name != "after-invalidate" {
		t.Fatalf("expected invalidation to force reload, got key=%+v", thirdKey)
	}
	if st.keyLookups != 2 || st.userLookups != 2 {
		t.Fatalf("expected reload after invalidation, keyLookups=%d userLookups=%d", st.keyLookups, st.userLookups)
	}
}

func TestCachedAPIKeyResolverReloadsAfterTTL(t *testing.T) {
	keyHash := "hash-for-ttl"
	st := &cacheResolverStore{
		key:  &store.APIKey{ID: "k1", UserID: "u1", Name: "before-expiry", Enabled: true},
		user: &store.User{ID: "u1", Enabled: true, TierID: "tier0-id"},
		tier: &store.Tier{ID: "tier0-id", Name: "tier0", RPM: 10, SuccessLimit: 800},
	}
	currentTime := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	resolver := NewCachedAPIKeyResolver(st, time.Second)
	t.Cleanup(resolver.Close)
	resolver.now = func() time.Time { return currentTime }

	if _, _, err := resolver.Resolve(context.Background(), keyHash); err != nil {
		t.Fatal(err)
	}
	st.key.Name = "after-expiry"
	currentTime = currentTime.Add(time.Second)

	key, _, err := resolver.Resolve(context.Background(), keyHash)
	if err != nil {
		t.Fatal(err)
	}
	if key.Name != "after-expiry" {
		t.Fatalf("expected TTL expiry to reload key, got %+v", key)
	}
	if st.keyLookups != 2 {
		t.Fatalf("expected two key lookups after TTL expiry, got %d", st.keyLookups)
	}
}

type cacheResolverStore struct {
	store.TestStore
	key         *store.APIKey
	user        *store.User
	tier        *store.Tier
	keyLookups  int
	userLookups int
}

func (s *cacheResolverStore) GetKeyByHash(context.Context, string) (*store.APIKey, error) {
	s.keyLookups++
	if s.key == nil {
		return nil, nil
	}
	keyCopy := *s.key
	return &keyCopy, nil
}

func (s *cacheResolverStore) GetUserByID(context.Context, string) (*store.User, error) {
	s.userLookups++
	if s.user == nil {
		return nil, store.ErrUserNotFound
	}
	userCopy := *s.user
	return &userCopy, nil
}

func (s *cacheResolverStore) GetTierByID(_ context.Context, tierID string) (*store.Tier, error) {
	if s.tier == nil || s.tier.ID != tierID {
		return nil, store.ErrTierNotFound
	}
	tierCopy := *s.tier
	return &tierCopy, nil
}

func (s *cacheResolverStore) GetTierByName(_ context.Context, tierName string) (*store.Tier, error) {
	if s.tier != nil && strings.EqualFold(s.tier.Name, tierName) {
		tierCopy := *s.tier
		return &tierCopy, nil
	}
	if strings.EqualFold(tierName, store.DefaultTierName) {
		return &store.Tier{ID: "tier0-id", Name: "tier0", RPM: 10, SuccessLimit: 800}, nil
	}
	return nil, nil
}

func generatedAPIKeyForTest(hexCharacter byte) string {
	return "grok_" + strings.Repeat(string(hexCharacter), 64)
}

func TestAPIKeyMiddlewareMissingTierReturnsInternalServerError(t *testing.T) {
	raw := generatedAPIKeyForTest('d')
	hash := keyhash.HashAPIKey(raw)
	st := &memStore{
		byHash: map[string]*store.APIKey{
			hash: {ID: "k1", UserID: "u1", Enabled: true},
		},
		users: map[string]*store.User{
			"u1": {ID: "u1", Enabled: true, TierID: "missing-tier"},
		},
	}
	// memStore GetTierByID defaults via TestStore to ErrTierNotFound
	h := APIKeyMiddleware(NewStoreAPIKeyResolver(st))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run when tier is missing")
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%q", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "authentication failed\n" {
		t.Fatalf("expected generic authentication error body, got %q", rec.Body.String())
	}
}
