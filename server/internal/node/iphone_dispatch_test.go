package node

import (
	"context"
	"strings"
	"testing"
)

// The iphone program answers the methods builtin.go routes to node/iphone and refuses any other.
func TestIPhoneBuiltinUnknownMethod(t *testing.T) {
	if _, err := callBuiltin(context.Background(), "iphone", "files", nil, nil); err == nil || !strings.Contains(err.Error(), "unknown method") {
		t.Fatalf("unknown method: %v", err)
	}
}
