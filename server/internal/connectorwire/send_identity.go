package connectorwire

import "context"

type sendIdentityKey struct{}

// WithSendIdentity carries an owner-selected sender across the manager's Inbox
// boundary. Only transports that explicitly support it may consume this value.
func WithSendIdentity(ctx context.Context, identity string) context.Context {
	return context.WithValue(ctx, sendIdentityKey{}, identity)
}

func SendIdentity(ctx context.Context) string {
	identity, _ := ctx.Value(sendIdentityKey{}).(string)
	return identity
}
