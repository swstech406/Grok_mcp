package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRegisterUserCreatesOnlyRegularUsersUnderConcurrency(t *testing.T) {
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
	if admins != 0 {
		t.Fatalf("self-registration should not create admins, got %d", admins)
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

func TestTryIncrementUserSuccessCallsDistinguishesMissingUserFromQuota(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()

	if err := s.TryIncrementUserSuccessCalls(ctx, "missing-user", 1); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("expected missing user error, got %v", err)
	}

	user, err := s.CreateUser(ctx, "quota-user", "hash", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TryIncrementUserSuccessCalls(ctx, user.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.TryIncrementUserSuccessCalls(ctx, user.ID, 1); !errors.Is(err, ErrQuotaSuccess) {
		t.Fatalf("expected quota error for existing exhausted user, got %v", err)
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
	tier, err := s.GetTierByName(ctx, "tier1")
	if err != nil || tier == nil {
		t.Fatalf("tier1 should be seeded by migration: %v", err)
	}
	updated, err := s.UpdateUser(ctx, u.ID, UserUpdates{TierID: &tier.ID})
	if err != nil {
		t.Fatal(err)
	}
	if updated.TierID != tier.ID {
		t.Fatalf("tier_id want %s got %s", tier.ID, updated.TierID)
	}
}

func TestUpdateUserAllowsAnyExistingTierID(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	user, err := s.CreateUser(ctx, "tier-target", "hash", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	customTier, err := s.CreateTier(ctx, "custom", 9, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.UpdateUser(ctx, user.ID, UserUpdates{TierID: &customTier.ID})
	if err != nil {
		t.Fatalf("any existing tier should be assignable, got %v", err)
	}
	if updated.TierID != customTier.ID {
		t.Fatalf("tier_id want %s got %s", customTier.ID, updated.TierID)
	}
}

func TestUpdateUserRejectsMissingOrEmptyTierID(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	user, err := s.CreateUser(ctx, "tier-target-2", "hash", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	missingTierID := "00000000-0000-4000-8000-missingtier"
	if _, err := s.UpdateUser(ctx, user.ID, UserUpdates{TierID: &missingTierID}); !errors.Is(err, ErrTierNotAssignable) {
		t.Fatalf("expected missing tier to be rejected, got %v", err)
	}
	emptyTierID := ""
	if _, err := s.UpdateUser(ctx, user.ID, UserUpdates{TierID: &emptyTierID}); !errors.Is(err, ErrTierNotAssignable) {
		t.Fatalf("expected empty tier to be rejected, got %v", err)
	}
}

func TestCreateAndRegisterUserFailClosedWithoutTier0(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	tier0, err := s.GetTierByName(ctx, "tier0")
	if err != nil || tier0 == nil {
		t.Fatalf("tier0 should be seeded by migration: %v", err)
	}
	if err := s.DeleteTier(ctx, tier0.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "without-tier0", "hash", RoleUser); !errors.Is(err, ErrTierNotFound) {
		t.Fatalf("expected CreateUser to fail closed without tier0, got %v", err)
	}
	if _, err := s.RegisterUser(ctx, "registered-without-tier0", "hash"); !errors.Is(err, ErrTierNotFound) {
		t.Fatalf("expected RegisterUser to fail closed without tier0, got %v", err)
	}
}

func TestSuccessQuotaResetsEachUTCMonth(t *testing.T) {
	s := openTestDB(t)
	januaryContext := WithSuccessQuotaNow(context.Background(), time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC))
	februaryContext := WithSuccessQuotaNow(context.Background(), time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC))

	user, err := s.CreateUser(januaryContext, "monthly-quota", "hash", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReserveSuccessCall(januaryContext, user.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.ReserveSuccessCall(januaryContext, user.ID, 1); !errors.Is(err, ErrQuotaSuccess) {
		t.Fatalf("expected January quota exhaustion, got %v", err)
	}

	if err := s.ReserveSuccessCall(februaryContext, user.ID, 1); err != nil {
		t.Fatalf("new month should reset quota before reserve: %v", err)
	}
	updatedUser, err := s.GetUserByID(februaryContext, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedUser.SuccessCalls != 1 || updatedUser.SuccessPeriod != "2026-02" {
		t.Fatalf("monthly reset should leave one February call, got calls=%d period=%q", updatedUser.SuccessCalls, updatedUser.SuccessPeriod)
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

func TestDeleteUserClearsInviteCodeCreatorReference(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	creator, err := store.CreateUser(ctx, "invite-creator", "hash", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	inviteCode, _, err := store.CreateInviteCode(ctx, creator.ID, 3)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteUser(ctx, creator.ID); err != nil {
		t.Fatal(err)
	}

	inviteCodes, err := store.ListInviteCodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(inviteCodes) != 1 || inviteCodes[0].ID != inviteCode.ID {
		t.Fatalf("invite code should remain after creator deletion, got %+v", inviteCodes)
	}
	if inviteCodes[0].CreatedByUserID != "" {
		t.Fatalf("deleted creator reference should be cleared, got %q", inviteCodes[0].CreatedByUserID)
	}
}

func TestCreateInviteCodeRejectsMissingCreator(t *testing.T) {
	store := openTestDB(t)

	if _, _, err := store.CreateInviteCode(context.Background(), "missing-creator", 1); err == nil {
		t.Fatal("expected missing invite-code creator to violate the foreign key")
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

func TestUpdateUserRejectsRemovingLastEnabledAdmin(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	admin, err := s.CreateUser(ctx, "only-enabled-admin", "hash", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}

	disabled := false
	if _, err := s.UpdateUser(ctx, admin.ID, UserUpdates{Enabled: &disabled}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("expected disabling last enabled admin to fail, got %v", err)
	}
	adminAfterDisableAttempt, err := s.GetUserByID(ctx, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !adminAfterDisableAttempt.Enabled || adminAfterDisableAttempt.Role != RoleAdmin {
		t.Fatalf("failed update must leave admin enabled, got enabled=%v role=%s", adminAfterDisableAttempt.Enabled, adminAfterDisableAttempt.Role)
	}

	regularUserRole := RoleUser
	if _, err := s.UpdateUser(ctx, admin.ID, UserUpdates{Role: &regularUserRole}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("expected downgrading last enabled admin to fail, got %v", err)
	}
	adminAfterRoleAttempt, err := s.GetUserByID(ctx, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !adminAfterRoleAttempt.Enabled || adminAfterRoleAttempt.Role != RoleAdmin {
		t.Fatalf("failed update must leave admin enabled, got enabled=%v role=%s", adminAfterRoleAttempt.Enabled, adminAfterRoleAttempt.Role)
	}
}

func TestUpdateUserAllowsRemovingAdminWhenAnotherEnabledAdminRemains(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	adminToDowngrade, err := s.CreateUser(ctx, "admin-to-downgrade", "hash", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "remaining-admin", "hash", RoleAdmin); err != nil {
		t.Fatal(err)
	}

	regularUserRole := RoleUser
	updated, err := s.UpdateUser(ctx, adminToDowngrade.ID, UserUpdates{Role: &regularUserRole})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Role != RoleUser {
		t.Fatalf("role after downgrade want %s got %s", RoleUser, updated.Role)
	}

	enabledAdminCount, err := s.CountEnabledAdmins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if enabledAdminCount != 1 {
		t.Fatalf("enabled admin count want 1 got %d", enabledAdminCount)
	}
}

func TestDeleteUserRejectsDeletingLastEnabledAdmin(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	enabledAdmin, err := s.CreateUser(ctx, "enabled-admin", "hash", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	disabledAdmin, err := s.CreateUser(ctx, "disabled-admin", "hash", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err := s.UpdateUser(ctx, disabledAdmin.ID, UserUpdates{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteUser(ctx, enabledAdmin.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("expected deleting last enabled admin to fail, got %v", err)
	}
}
