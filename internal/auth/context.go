package auth

import (
	"context"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

// ctxKey 为 context 中存放 APIKey 的私有键类型，避免与其他包冲突。
type ctxKey struct{}

// WithAPIKey 将鉴权通过的密钥附加到 context，供限流、用量等中间件读取。
func WithAPIKey(ctx context.Context, key *store.APIKey) context.Context {
	return context.WithValue(ctx, ctxKey{}, key)
}

// APIKeyFromContext 返回 APIKeyMiddleware 注入的密钥；第二个值为 false 表示当前请求未经过 API Key 鉴权。
func APIKeyFromContext(ctx context.Context) (*store.APIKey, bool) {
	k, ok := ctx.Value(ctxKey{}).(*store.APIKey)
	return k, ok
}
