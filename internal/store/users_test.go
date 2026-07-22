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
	userPage, err := s.ListUsersPage(ctx, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	users := userPage.Users
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

	if _, err := s.ReserveSuccessCall(ctx, u1.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveSuccessCall(ctx, u1.ID, 1); !errors.Is(err, ErrQuotaSuccess) {
		t.Fatalf("expected success quota, got %v", err)
	}
}

func TestReserveSuccessCallDistinguishesMissingUserFromQuota(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()

	if _, err := s.ReserveSuccessCall(ctx, "missing-user", 1); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("expected missing user error, got %v", err)
	}

	user, err := s.CreateUser(ctx, "quota-user", "hash", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveSuccessCall(ctx, user.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveSuccessCall(ctx, user.ID, 1); !errors.Is(err, ErrQuotaSuccess) {
		t.Fatalf("expected quota error for existing exhausted user, got %v", err)
	}
}

func TestSuccessQuotaOperationsExposeLatencyAndOutcomeMetrics(t *testing.T) {
	sqliteStore := openTestDB(t)
	sqliteStore.SetMetricsEnabled(true)
	ctx := context.Background()
	userID := testUserID(t, sqliteStore)

	reservation, err := sqliteStore.ReserveSuccessCall(ctx, userID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqliteStore.ReserveSuccessCall(ctx, userID, 1); !errors.Is(err, ErrQuotaSuccess) {
		t.Fatalf("second reservation error = %v, want ErrQuotaSuccess", err)
	}
	if err := sqliteStore.ReleaseSuccessCall(ctx, reservation); err != nil {
		t.Fatal(err)
	}

	metrics := sqliteStore.SQLiteMetrics()
	if metrics.QuotaReserve.Attempts != 2 || metrics.QuotaLimitRejections != 1 {
		t.Fatalf("unexpected quota reserve metrics: %+v", metrics)
	}
	if metrics.QuotaReserve.Errors != 0 {
		t.Fatalf("quota exhaustion must not count as a database error: %+v", metrics.QuotaReserve)
	}
	if metrics.QuotaRelease.Attempts != 1 || metrics.QuotaRelease.Errors != 0 {
		t.Fatalf("unexpected quota release metrics: %+v", metrics.QuotaRelease)
	}
	if metrics.PrimaryWritePool.MaximumOpenConnections != 1 {
		t.Fatalf("primary write pool max connections = %d, want 1", metrics.PrimaryWritePool.MaximumOpenConnections)
	}
}

func TestReserveAndReleaseSuccessCall(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	u, err := s.CreateUser(ctx, "u2", "h", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := s.ReserveSuccessCall(ctx, u.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveSuccessCall(ctx, u.ID, 1); !errors.Is(err, ErrQuotaSuccess) {
		t.Fatalf("expected success quota on reserve, got %v", err)
	}
	if err := s.ReleaseSuccessCall(ctx, reservation); err != nil {
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
	if _, err := s.ReserveSuccessCall(januaryContext, user.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveSuccessCall(januaryContext, user.ID, 1); !errors.Is(err, ErrQuotaSuccess) {
		t.Fatalf("expected January quota exhaustion, got %v", err)
	}

	if _, err := s.ReserveSuccessCall(februaryContext, user.ID, 1); err != nil {
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

func TestReleaseSuccessCallPreservesLaterMonthReservation(t *testing.T) {
	sqliteStore := openTestDB(t)
	januaryContext := WithSuccessQuotaNow(context.Background(), time.Date(2026, time.January, 31, 23, 59, 0, 0, time.UTC))
	februaryContext := WithSuccessQuotaNow(context.Background(), time.Date(2026, time.February, 1, 0, 1, 0, 0, time.UTC))

	user, err := sqliteStore.CreateUser(januaryContext, "cross-month-release", "hash", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	januaryReservation, err := sqliteStore.ReserveSuccessCall(januaryContext, user.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if januaryReservation.UserID != user.ID || januaryReservation.Period != "2026-01" {
		t.Fatalf("January reservation = %+v, want user=%q period=2026-01", januaryReservation, user.ID)
	}
	februaryReservation, err := sqliteStore.ReserveSuccessCall(februaryContext, user.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if februaryReservation.UserID != user.ID || februaryReservation.Period != "2026-02" {
		t.Fatalf("February reservation = %+v, want user=%q period=2026-02", februaryReservation, user.ID)
	}

	if err := sqliteStore.ReleaseSuccessCall(februaryContext, januaryReservation); err != nil {
		t.Fatal(err)
	}
	updatedUser, err := sqliteStore.GetUserByID(februaryContext, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedUser.SuccessCalls != 1 || updatedUser.SuccessPeriod != "2026-02" {
		t.Fatalf("releasing January must preserve February reservation, got calls=%d period=%q", updatedUser.SuccessCalls, updatedUser.SuccessPeriod)
	}
}

func TestReleaseSuccessCallRejectsInvalidReservation(t *testing.T) {
	sqliteStore := openTestDB(t)
	requestContext := context.Background()
	user, err := sqliteStore.CreateUser(requestContext, "invalid-release-token", "hash", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqliteStore.ReserveSuccessCall(requestContext, user.ID, 2); err != nil {
		t.Fatal(err)
	}

	invalidReservation := SuccessQuotaReservation{UserID: user.ID, Period: "January"}
	if err := sqliteStore.ReleaseSuccessCall(requestContext, invalidReservation); err == nil {
		t.Fatal("invalid reservation should be rejected")
	}
	updatedUser, err := sqliteStore.GetUserByID(requestContext, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedUser.SuccessCalls != 1 {
		t.Fatalf("invalid reservation changed success_calls to %d, want 1", updatedUser.SuccessCalls)
	}
}

func TestDeleteUserRemovesUserKeysAndUsage(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	user, err := s.CreateUser(ctx, "delete-me", "hash", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := s.CreateKey(ctx, user.ID, "temporary key", 20)
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
	keyPage, err := s.ListKeysByUserPage(ctx, user.ID, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	keys := keyPage.Keys
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

func TestDeleteUserSucceedsWhenDebugCleanupFails(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	user, err := store.CreateUser(ctx, "debug-cleanup-failure", "hash", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateKey(ctx, user.ID, "temporary key", 20); err != nil {
		t.Fatal(err)
	}
	if err := store.debugDB.Close(); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteUser(ctx, user.ID); err != nil {
		t.Fatalf("primary deletion should succeed despite debug cleanup failure: %v", err)
	}
	if _, err := store.GetUserByID(ctx, user.ID); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("expected user to be deleted from the primary database, got %v", err)
	}
	keyPage, err := store.ListKeysByUserPage(ctx, user.ID, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	keys := keyPage.Keys
	if len(keys) != 0 {
		t.Fatalf("expected user keys to be deleted, got %d", len(keys))
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

	inviteCodePage, err := store.ListInviteCodesPage(ctx, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	inviteCodes := inviteCodePage.InviteCodes
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

func TestInviteCodeExistsDoesNotReplaceTransactionalValidation(t *testing.T) {
	sqliteStore := openTestDB(t)
	ctx := context.Background()

	creator, err := sqliteStore.CreateUser(ctx, "invite-precheck-creator", "hash", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	inviteCode, rawInviteCode, err := sqliteStore.CreateInviteCode(ctx, creator.ID, 1)
	if err != nil {
		t.Fatal(err)
	}

	inviteCodeExists, err := sqliteStore.InviteCodeExists(ctx, rawInviteCode)
	if err != nil {
		t.Fatal(err)
	}
	if !inviteCodeExists {
		t.Fatal("created invite code should be found by the existence precheck")
	}
	invalidInviteCodeExists, err := sqliteStore.InviteCodeExists(ctx, "invalid-code")
	if err != nil {
		t.Fatal(err)
	}
	if invalidInviteCodeExists {
		t.Fatal("unknown invite code should not pass the existence precheck")
	}

	disabled := false
	if _, err := sqliteStore.UpdateInviteCode(ctx, inviteCode.ID, InviteCodeUpdates{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	inviteCodeStillExists, err := sqliteStore.InviteCodeExists(ctx, rawInviteCode)
	if err != nil {
		t.Fatal(err)
	}
	if !inviteCodeStillExists {
		t.Fatal("disabled invite code should still pass the non-authoritative existence precheck")
	}
	if _, err := sqliteStore.RegisterUserWithInviteCode(ctx, "invite-precheck-user", "hash", rawInviteCode); !errors.Is(err, ErrInviteCodeDisabled) {
		t.Fatalf("transactional registration should reject disabled invite code, got %v", err)
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
