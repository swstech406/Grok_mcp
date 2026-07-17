package store

import (
	"context"
	"errors"
	"testing"
)

func persistRegistrationModeForTest(t *testing.T, sqliteStore *SQLiteStore, registrationMode RegistrationMode) {
	t.Helper()
	now := formatTime(nowUTC())
	_, err := sqliteStore.db.Exec(`
		INSERT INTO server_settings (
			id, cpa_base_url, cpa_api_key_ciphertext, cpa_api_key_nonce, cpa_api_key_encryption_version,
			upstream_protocol, model, timeout_seconds, mcp_global_search_concurrency, mcp_user_search_concurrency,
			proxy_url, proxy_enabled, registration_mode, debug, operations_metrics_enabled, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET registration_mode = excluded.registration_mode, updated_at = excluded.updated_at`,
		serverSettingsID,
		"https://example.com",
		"ciphertext",
		"nonce",
		1,
		"responses",
		"grok-test",
		30,
		4,
		1,
		"",
		0,
		string(registrationMode),
		0,
		0,
		now,
		now,
	)
	if err != nil {
		t.Fatalf("persist registration mode: %v", err)
	}
}

func TestRegisterUserWithCurrentModeRejectsStaleFreePolicy(t *testing.T) {
	testCases := []struct {
		name          string
		currentMode   RegistrationMode
		expectedError error
	}{
		{
			name:          "registration disabled",
			currentMode:   RegistrationModeDisabled,
			expectedError: ErrRegistrationDisabled,
		},
		{
			name:          "invite required",
			currentMode:   RegistrationModeInvite,
			expectedError: ErrInviteCodeInvalid,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			sqliteStore := openTestDB(t)
			persistRegistrationModeForTest(t, sqliteStore, testCase.currentMode)

			_, err := sqliteStore.RegisterUserWithCurrentMode(
				context.Background(),
				"stale-free-user",
				"password-hash",
				"",
				RegistrationModeFree,
			)
			if !errors.Is(err, testCase.expectedError) {
				t.Fatalf("registration error = %v, want %v", err, testCase.expectedError)
			}

			registeredUser, lookupErr := sqliteStore.GetUserByUsername(context.Background(), "stale-free-user")
			if lookupErr != nil {
				t.Fatal(lookupErr)
			}
			if registeredUser != nil {
				t.Fatalf("stale free policy created user: %+v", registeredUser)
			}
		})
	}
}

func TestRegisterUserWithCurrentModeUsesFreePolicyWithoutConsumingInvite(t *testing.T) {
	sqliteStore := openTestDB(t)
	ctx := context.Background()
	administrator, err := sqliteStore.CreateUser(ctx, "registration-admin", "hash", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	inviteCode, rawInviteCode, err := sqliteStore.CreateInviteCode(ctx, administrator.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	persistRegistrationModeForTest(t, sqliteStore, RegistrationModeFree)

	registeredUser, err := sqliteStore.RegisterUserWithCurrentMode(
		ctx,
		"current-free-user",
		"password-hash",
		rawInviteCode,
		RegistrationModeInvite,
	)
	if err != nil {
		t.Fatal(err)
	}
	if registeredUser == nil || registeredUser.Username != "current-free-user" {
		t.Fatalf("unexpected registered user: %+v", registeredUser)
	}

	storedInviteCode, err := sqliteStore.getInviteCodeByID(ctx, inviteCode.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedInviteCode.RegistrationCount != 0 {
		t.Fatalf("free registration consumed invite code %d time(s)", storedInviteCode.RegistrationCount)
	}
}

func TestRegisterUserWithCurrentModeSupportsInitialFreeAndInviteModes(t *testing.T) {
	t.Run("free", func(t *testing.T) {
		sqliteStore := openTestDB(t)
		registeredUser, err := sqliteStore.RegisterUserWithCurrentMode(
			context.Background(),
			"initial-free-user",
			"password-hash",
			"",
			RegistrationModeFree,
		)
		if err != nil {
			t.Fatal(err)
		}
		if registeredUser == nil || registeredUser.Username != "initial-free-user" {
			t.Fatalf("unexpected registered user: %+v", registeredUser)
		}
	})

	t.Run("invite", func(t *testing.T) {
		sqliteStore := openTestDB(t)
		ctx := context.Background()
		administrator, err := sqliteStore.CreateUser(ctx, "initial-invite-admin", "hash", RoleAdmin)
		if err != nil {
			t.Fatal(err)
		}
		inviteCode, rawInviteCode, err := sqliteStore.CreateInviteCode(ctx, administrator.ID, 1)
		if err != nil {
			t.Fatal(err)
		}

		registeredUser, err := sqliteStore.RegisterUserWithCurrentMode(
			ctx,
			"initial-invite-user",
			"password-hash",
			rawInviteCode,
			RegistrationModeInvite,
		)
		if err != nil {
			t.Fatal(err)
		}
		if registeredUser == nil || registeredUser.Username != "initial-invite-user" {
			t.Fatalf("unexpected registered user: %+v", registeredUser)
		}

		storedInviteCode, err := sqliteStore.getInviteCodeByID(ctx, inviteCode.ID)
		if err != nil {
			t.Fatal(err)
		}
		if storedInviteCode.RegistrationCount != 1 {
			t.Fatalf("invite registration count = %d, want 1", storedInviteCode.RegistrationCount)
		}
	})
}
