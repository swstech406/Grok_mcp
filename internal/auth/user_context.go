package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

// UserLoader loads a persisted user by ID.
// Defined at the consumer side so auth does not require the full store.Store surface.
type UserLoader interface {
	GetUserByID(ctx context.Context, id string) (*store.User, error)
}

// TierLoader loads the assigned tier for effective-limit resolution.
type TierLoader interface {
	GetTierByID(ctx context.Context, id string) (*store.Tier, error)
}

// UserTierLoader is the minimal store surface needed to build an AuthenticatedUser.
type UserTierLoader interface {
	UserLoader
	TierLoader
}

// AuthenticatedUser is the request-scoped view of a user after tier limits are applied.
// It embeds the persisted store.User and adds effective RPM / SuccessLimit from the tier.
type AuthenticatedUser struct {
	store.User
	// RPM is the effective requests-per-minute limit from the assigned tier.
	// 0 means unlimited; negative values are treated as misconfiguration by rate limiting.
	RPM int
	// SuccessLimit is the effective monthly successful tools/call limit from the tier.
	// 0 means unlimited.
	SuccessLimit int
}

type userCtxKey struct{}

// TierResolutionError retains the assigned tier identifier for safe operator
// diagnostics while keeping the ordinary error string free of internal data.
type TierResolutionError struct {
	TierID string
	Cause  error
}

func (tierResolutionError *TierResolutionError) Error() string {
	return "assigned tier is unavailable"
}

func (tierResolutionError *TierResolutionError) Unwrap() error {
	if tierResolutionError == nil {
		return nil
	}
	return tierResolutionError.Cause
}

// WithUser attaches an authenticated user (with effective limits) to the request context.
func WithUser(ctx context.Context, user *AuthenticatedUser) context.Context {
	return context.WithValue(ctx, userCtxKey{}, user)
}

// UserFromContext returns the authenticated user injected by MCP API Key or panel JWT middleware.
func UserFromContext(ctx context.Context) (*AuthenticatedUser, bool) {
	user, ok := ctx.Value(userCtxKey{}).(*AuthenticatedUser)
	return user, ok
}

// LoadUserWithTierLimits loads a persisted user and returns an AuthenticatedUser whose
// effective limits come solely from the assigned tier. Missing assignments and
// missing tiers fail closed so zero limits cannot be mistaken for unlimited.
func LoadUserWithTierLimits(ctx context.Context, loader UserTierLoader, userID string) (*AuthenticatedUser, error) {
	user, err := loader.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return AuthenticateUser(ctx, loader, user)
}

// AuthenticateUser builds an AuthenticatedUser from a already-loaded persisted user.
func AuthenticateUser(ctx context.Context, loader TierLoader, user *store.User) (*AuthenticatedUser, error) {
	if user == nil {
		return nil, fmt.Errorf("user is required")
	}
	tier, err := resolveTier(ctx, loader, user)
	if err != nil {
		return nil, err
	}
	return &AuthenticatedUser{
		User:         *user,
		RPM:          tier.RPM,
		SuccessLimit: tier.SuccessLimit,
	}, nil
}

// resolveTier requires the persisted user to reference an existing tier.
func resolveTier(ctx context.Context, loader TierLoader, user *store.User) (*store.Tier, error) {
	if user == nil {
		return nil, fmt.Errorf("user is required")
	}
	tierID := strings.TrimSpace(user.TierID)
	if tierID == "" {
		return nil, store.ErrTierNotAssignable
	}
	tier, err := loader.GetTierByID(ctx, tierID)
	if err != nil {
		if errors.Is(err, store.ErrTierNotFound) {
			return nil, &TierResolutionError{TierID: tierID, Cause: store.ErrTierNotFound}
		}
		return nil, err
	}
	if tier == nil {
		return nil, &TierResolutionError{TierID: tierID, Cause: store.ErrTierNotFound}
	}
	return tier, nil
}
