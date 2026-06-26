package auth

import (
	"context"

	"github.com/grok-mcp/internal/store"
)

type userCtxKey struct{}

// WithUser 将所属用户写入 context（MCP 鉴权链在加载 API Key 后设置）。
func WithUser(ctx context.Context, user *store.User) context.Context {
	return context.WithValue(ctx, userCtxKey{}, user)
}

// UserFromContext 返回 MCP 或面板 JWT 链注入的用户。
func UserFromContext(ctx context.Context) (*store.User, bool) {
	u, ok := ctx.Value(userCtxKey{}).(*store.User)
	return u, ok
}

// LoadUserWithTierLimits 加载用户，并把所属 tier 的限额（rpm/total_limit/success_limit）
// 合并进返回的 user 对象。限额以 tier 为唯一来源，用户自身限额字段不再作为执行依据。
func LoadUserWithTierLimits(ctx context.Context, st store.Store, userID string) (*store.User, error) {
	user, err := st.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	applyTierLimits(ctx, st, user)
	return user, nil
}

// applyTierLimits 把 user.TierID 对应 tier 的限额就地写进 user；tier 缺失则保持原值。
func applyTierLimits(ctx context.Context, st store.Store, user *store.User) {
	if user == nil || user.TierID == "" {
		return
	}
	tier, err := st.GetTierByID(ctx, user.TierID)
	if err == nil && tier != nil {
		user.RPM = tier.RPM
		user.TotalLimit = tier.TotalLimit
		user.SuccessLimit = tier.SuccessLimit
	}
}
