// Package buswire binds bus and RPC method names to their request and reply types.
//
//persona:layer contract
package buswire

import "context"

// Method names a request with parameters P and a reply R.
type Method[P, R any] struct{ Name string }

// Event names a one-way message with payload P.
type Event[P any] struct{ Name string }

// None represents an empty request or acknowledgment. Its wire form is {}.
type None struct{}

// Caller is the request interface shared by the bus client and RPC peer.
type Caller interface {
	Call(ctx context.Context, method string, params, out any) error
}

// Call sends typed parameters and decodes a typed reply using c's wire codec.
// For a None reply, it waits for the acknowledgment or error with no decode destination.
func Call[P, R any](ctx context.Context, c Caller, m Method[P, R], params P) (R, error) {
	var result R
	if _, ok := any(result).(None); ok {
		return result, c.Call(ctx, m.Name, params, nil)
	}
	err := c.Call(ctx, m.Name, params, &result)
	return result, err
}
