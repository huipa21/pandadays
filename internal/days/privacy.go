package days

import "context"

type privateContextKey struct{}
type includePrivateContextKey struct{}

// WithPrivateIncluded includes private days in lists after Vault authorization.
// It does not change the namespace used for individual days, media, or exports.
func WithPrivateIncluded(ctx context.Context) context.Context {
	return context.WithValue(ctx, includePrivateContextKey{}, true)
}

func IncludesPrivate(ctx context.Context) bool {
	include, _ := ctx.Value(includePrivateContextKey{}).(bool)
	return include
}

// WithPrivate selects the private namespace after the caller has authorized Vault access.
func WithPrivate(ctx context.Context, private bool) context.Context {
	return context.WithValue(ctx, privateContextKey{}, private)
}

func IsPrivate(ctx context.Context) bool {
	private, _ := ctx.Value(privateContextKey{}).(bool)
	return private
}
