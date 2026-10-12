package middleware

import (
	"context"

	"github.com/gin-gonic/gin"
)

// ClientMetadata stashes the X-Client-* headers on the request context so
// downstream handlers and the request logger can read them via
// ClientMetadataFromContext.
func ClientMetadata() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		if v := c.GetHeader(HeaderClientPlatform); v != "" {
			ctx = context.WithValue(ctx, ctxKeyClientPlatform, v)
		}
		if v := c.GetHeader(HeaderClientVersion); v != "" {
			ctx = context.WithValue(ctx, ctxKeyClientVersion, v)
		}
		if v := c.GetHeader(HeaderClientOS); v != "" {
			ctx = context.WithValue(ctx, ctxKeyClientOS, v)
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// ClientMetadataFromContext returns the platform/version/os captured from the
// X-Client-* headers. Empty strings mean the client did not send the header.
func ClientMetadataFromContext(ctx context.Context) (platform, version, os string) {
	platform, _ = ctx.Value(ctxKeyClientPlatform).(string)
	version, _ = ctx.Value(ctxKeyClientVersion).(string)
	os, _ = ctx.Value(ctxKeyClientOS).(string)
	return platform, version, os
}

// SetClientMetadata attaches client metadata to a context directly, for
// transports where it arrives outside HTTP headers (the realtime hub's query
// parameters).
func SetClientMetadata(ctx context.Context, platform, version, os string) context.Context {
	if platform != "" {
		ctx = context.WithValue(ctx, ctxKeyClientPlatform, platform)
	}
	if version != "" {
		ctx = context.WithValue(ctx, ctxKeyClientVersion, version)
	}
	if os != "" {
		ctx = context.WithValue(ctx, ctxKeyClientOS, os)
	}
	return ctx
}
