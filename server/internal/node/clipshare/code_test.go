package clipshare

import (
	"encoding/json"
	"errors"

	"github.com/nwverse/persona/server/internal/rpc"
)

// rpcCode is the code of an rpc error, "" for anything else.
func rpcCode(err error) string {
	var e *rpc.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
