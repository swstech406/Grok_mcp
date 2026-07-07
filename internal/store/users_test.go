package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRegisterUserOnlyOneAdminUnderConcurrency(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("user%d", i)
			_, err := s.RegisterUser(ctx, name, "hash")
			if err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	users, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != n {
		t.Fatalf("want %d users got %d", n, len(users))
	}
	var admins int
	for _, u := range users {
		if u.Role == RoleAdmin {
			admins++
		}
	}
	if admins != 1 {
		t.Fatalf("want exactly 1 admin got %d", admins)
	}
}

func TestFirstUserAdminAndSuccessQuotaReserve(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()

	u1, err := s.CreateUser(ctx, "admin1", "hash", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if u1.Role != RoleAdmin {
		t.Fatalf("role %s", u1.Role)
	}

	if err := s.TryIncrementUserSuccessCalls(ctx, u1.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.TryIncrementUserSuccessCalls(ctx, u1.ID, 1); !errors.Is(err, ErrQuotaSuccess) {
		t.Fatalf("expected success quota, got %v", err)
	}
}

func TestReserveAndReleaseSuccessCall(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	u, err := s.CreateUser(ctx, "u2", "h", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReserveSuccessCall(ctx, u.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.ReserveSuccessCall(ctx, u.ID, 1); !errors.Is(err, ErrQuotaSuccess) {
		t.Fatalf("expected success quota on reserve, got %v", err)
	}
	if err := s.ReleaseSuccessCall(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	uAfter, _ := s.GetUserByID(ctx, u.ID)
	if uAfter.SuccessCalls != 0 {
		t.Fatalf("success_calls after release want 0 got %d", uAfter.SuccessCalls)
	}
}

func TestUpdateUserChangesTierID(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	u, err := s.CreateUser(ctx, "u", "h", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	tier, err := s.CreateTier(ctx, "t", 0, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.UpdateUser(ctx, u.ID, UserUpdates{TierID: &tier.ID})
	if err != nil {
		t.Fatal(err)
	}
	if updated.TierID != tier.ID {
		t.Fatalf("tier_id want %s got %s", tier.ID, updated.TierID)
	}
}

func TestDeleteUserRemovesUserKeysAndUsage(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	user, err := s.CreateUser(ctx, "delete-me", "hash", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := s.CreateKey(ctx, user.ID, "temporary key")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsage(ctx, UsageRecord{
		KeyID:      key.ID,
		ToolName:   "grok_web_search",
		Timestamp:  time.Now().UTC(),
		DurationMs: 25,
		Success:    true,
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetUserByID(ctx, user.ID); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("expected deleted user to be missing, got %v", err)
	}
	keys, err := s.ListKeysByUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected user keys to be deleted, got %d", len(keys))
	}
	stats, err := s.GetGlobalStats(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalCalls != 0 {
		t.Fatalf("expected deleted user usage to be deleted, got %d calls", stats.TotalCalls)
	}
}

func TestDeleteUserRejectsLastAdmin(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	admin, err := s.CreateUser(ctx, "only-admin", "hash", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, admin.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("expected last admin deletion to fail, got %v", err)
	}
}
