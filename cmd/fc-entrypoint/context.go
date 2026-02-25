package main

import "context"

type ctxKey struct{}

type AppContext struct {
	RequestID string
}

// WithContext 注入 appContext
func WithContext(ctx context.Context, appContext AppContext) context.Context {
	return context.WithValue(ctx, ctxKey{}, appContext)
}

// GetAppContext 获取 appContext
func GetAppContext(ctx context.Context) AppContext {
	v := ctx.Value(ctxKey{})
	if v == nil {
		return AppContext{}
	}
	return v.(AppContext)
}