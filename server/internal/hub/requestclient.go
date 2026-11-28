package hub

import (
	"context"
	"strings"
)

type requestClient struct {
	Platform string
	Version  string
	Instance string
}

type requestClientKey struct{}

func requestClientOf(ctx context.Context) requestClient {
	client, _ := ctx.Value(requestClientKey{}).(requestClient)
	return client
}

// parseRequestClient accepts a bounded platform/version token, so headers cannot inject log lines.
func parseRequestClient(value string) requestClient {
	value = strings.TrimSpace(value)
	platform, version, ok := strings.Cut(value, "/")
	if !ok || len(value) > 128 || platform == "" || version == "" {
		return requestClient{}
	}
	for _, part := range []string{platform, version} {
		if strings.Trim(part, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._+-") != "" {
			return requestClient{}
		}
	}
	return requestClient{Platform: platform, Version: version}
}

// parseRequestClientInstance binds web REST requests to the same tab's hello.
// It is an opaque, bounded token, never a credential or a durable device ID.
func parseRequestClientInstance(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || strings.Trim(value, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._+-") != "" {
		return ""
	}
	return value
}
