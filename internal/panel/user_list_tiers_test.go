package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

type userListTierStore struct {
	store.TestStore

	userPage            *store.UserPage
	tierByID            map[string]*store.Tier
	requestedTierIDs    []string
	batchTierLoadCalls  int
	singleTierLoadCalls int
}

func (testStore *userListTierStore) ListUsersPage(context.Context, *store.TimeIDCursor, int) (*store.UserPage, error) {
	return testStore.userPage, nil
}

func (testStore *userListTierStore) GetTiersByIDs(_ context.Context, tierIDs []string) (map[string]*store.Tier, error) {
	testStore.batchTierLoadCalls++
	testStore.requestedTierIDs = append([]string(nil), tierIDs...)
	return testStore.tierByID, nil
}

func (testStore *userListTierStore) GetTierByID(context.Context, string) (*store.Tier, error) {
	testStore.singleTierLoadCalls++
	return nil, store.ErrTierNotFound
}

func TestAdminListUsersLoadsUniqueTiersInOneBatch(t *testing.T) {
	firstTier := &store.Tier{ID: "tier-one", Name: "tier1", Level: 1, RPM: 10, SuccessLimit: 100}
	secondTier := &store.Tier{ID: "tier-two", Name: "tier2", Level: 2, RPM: 20, SuccessLimit: 200}
	testStore := &userListTierStore{
		userPage: &store.UserPage{
			Users: []*store.User{
				{ID: "user-one", Username: "one", TierID: firstTier.ID},
				{ID: "user-two", Username: "two", TierID: firstTier.ID},
				{ID: "user-three", Username: "three", TierID: "  " + secondTier.ID + "  "},
			},
			TotalCount: 3,
		},
		tierByID: map[string]*store.Tier{
			firstTier.ID:  firstTier,
			secondTier.ID: secondTier,
		},
	}
	handler := &Handler{Store: testStore}
	request := httptest.NewRequest(http.MethodGet, "/panel/v1/admin/users?limit=50", nil)
	responseRecorder := httptest.NewRecorder()

	handler.adminListUsers(responseRecorder, request)

	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("admin list users returned status %d: %s", responseRecorder.Code, responseRecorder.Body.String())
	}
	if testStore.batchTierLoadCalls != 1 {
		t.Fatalf("batch tier load calls = %d, want 1", testStore.batchTierLoadCalls)
	}
	if testStore.singleTierLoadCalls != 0 {
		t.Fatalf("single tier load calls = %d, want 0", testStore.singleTierLoadCalls)
	}
	if len(testStore.requestedTierIDs) != 2 || testStore.requestedTierIDs[0] != firstTier.ID || testStore.requestedTierIDs[1] != secondTier.ID {
		t.Fatalf("requested tier IDs = %v, want [%s %s]", testStore.requestedTierIDs, firstTier.ID, secondTier.ID)
	}

	var response UsersResponse
	if err := json.NewDecoder(responseRecorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Users) != 3 {
		t.Fatalf("response user count = %d, want 3", len(response.Users))
	}
	if response.Users[0].TierName != firstTier.Name || response.Users[1].TierName != firstTier.Name || response.Users[2].TierName != secondTier.Name {
		t.Fatalf("unexpected response tiers: %+v", response.Users)
	}
}
