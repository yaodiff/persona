package rpc

// Marshaled is params exactly as json.Marshal wrote them: compact, HTML characters, U+2028 and U+2029 escaped, valid
// UTF-8 only. Notify puts them into its line as they are. As json.RawMessage the envelope's encoder checks them once
// more, finds nothing to change (it keeps the escapes), and copies them twice on the way (a computer's session list,
// each time it changes). Bytes from anywhere else (a runtime's push, a file) go as json.RawMessage, checked as before.
type Marshaled []byte

const eventHead, eventMid, eventTail = `{"event":"`, `","params":`, "}\n"

// marshaledLineSize is the encoded line size of {"event":<event>,"params":<params>}\n when marshaledLine can write it
// directly without falling back to the JSON encoder.
func marshaledLineSize(event string, params Marshaled) (int, bool) {
	if !plain(event) || len(params) == 0 {
		return 0, false
	}
	return len(eventHead) + len(event) + len(eventMid) + len(params) + len(eventTail), true
}

func marshaledLineWithSize(event string, params Marshaled, size int) []byte {
	b := make([]byte, 0, size)
	b = append(append(append(b, eventHead...), event...), eventMid...)
	return append(append(b, params...), eventTail...)
}

// marshaledLine is the line the envelope's encoder and a newline give for event and params, in one buffer of its
// size: {"event":<event>,"params":<params>}. False when event is empty (the envelope leaves it out) or has a byte the
// encoder may escape, or params is empty (an error): the encoder writes those.
func marshaledLine(event string, params Marshaled) ([]byte, bool) {
	size, ok := marshaledLineSize(event, params)
	if !ok {
		return nil, false
	}
	return marshaledLineWithSize(event, params, size), true
}

// plain: json.Marshal writes s as it is between its quotes, and does not leave it out (it is not empty).
func plain(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c >= 0x7f || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' {
			return false
		}
	}
	return true
}

// Appended is a Call's params written straight into its line: Append adds Size bytes, the params exactly as Call
// writes them for a value (Marshal: compact, U+2028 and U+2029 escaped, valid UTF-8). A relayed upload is most of its
// request's params, in base64: as a value, the encoder writes it into a buffer of its own and copies it out, and the
// line's encoder checks and copies it once more; appended, the line is the only copy.
type Appended struct {
	Size   int
	Append func([]byte) []byte
}

// Marshal is v as Call and Notify write it for params: json.Marshal's bytes with < > & as they are (marshal). The
// parts of Appended params that are not appended by hand are made with it, so the line is the one a value gives.
func Marshal(v any) ([]byte, error) { return marshal(v) }

// callRawSize is the size of {"id":<id>,"method":<method>,"params":<raw>}\n when id and method are plain JSON strings.
func callRawSize(id, method string, rawLen int) (int, bool) {
	if !plain(id) || !plain(method) || rawLen <= 0 {
		return 0, false
	}
	return len(callHead) + len(id) + len(callMid) + len(method) + len(callParams) + rawLen + len("}\n"), true
}

// callSize is the size of the line callLine writes, and whether it writes it: {"id":<id>,"method":<method>,
// "params":<params>} and a newline, as json.Marshal writes the envelope. Not when id or method has a byte the encoder
// may escape or is empty (the envelope leaves it out), or there are no params (an error): the encoder writes those.
func callSize(id, method string, params Appended) (int, bool) {
	return callRawSize(id, method, params.Size)
}

const callHead, callMid, callParams = `{"id":"`, `","method":"`, `","params":`

const replyHead, replyResult = `{"id":"`, `","result":`

// replyResultSize is the size of {"id":<id>,"result":<raw>}\n when id is a plain JSON string and raw is already encoded.
func replyResultSize(id string, rawLen int) (int, bool) {
	if !plain(id) || rawLen <= 0 {
		return 0, false
	}
	return len(replyHead) + len(id) + len(replyResult) + rawLen + len("}\n"), true
}

// callLine is that line, in one buffer of size bytes (callSize's).
func callLine(id, method string, params Appended, size int) []byte {
	b := make([]byte, 0, size)
	b = append(append(append(append(append(b, callHead...), id...), callMid...), method...), callParams...)
	return append(params.Append(b), "}\n"...)
}

// MarshalJSON is m as it is, as json.RawMessage's: params of a Call or a nested value are not written as base64.
func (m Marshaled) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}
	return m, nil
}
