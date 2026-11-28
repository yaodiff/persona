package proto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strings"
)

// CanonicalJSON encodes one complete JSON value with deterministic object keys
// and numeric values independent of their spelling. In particular, a PostgreSQL
// JSONB round trip (1e2 to 100, or 1.0 to 1) cannot change an operation's identity.
func CanonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	value, err := canonicalJSONNumbers(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func canonicalJSONNumbers(value any) (any, error) {
	switch v := value.(type) {
	case json.Number:
		return canonicalJSONNumber(v)
	case []any:
		for i, item := range v {
			normalized, err := canonicalJSONNumbers(item)
			if err != nil {
				return nil, err
			}
			v[i] = normalized
		}
	case map[string]any:
		for key, item := range v {
			normalized, err := canonicalJSONNumbers(item)
			if err != nil {
				return nil, err
			}
			v[key] = normalized
		}
	}
	return value, nil
}

// canonicalJSONNumber uses one exact scientific form for every decimal value.
// A big integer exponent avoids overflow without converting through float64.
func canonicalJSONNumber(number json.Number) (json.Number, error) {
	raw := number.String()
	mantissa, exponentText := raw, ""
	if at := strings.IndexAny(raw, "eE"); at >= 0 {
		mantissa, exponentText = raw[:at], raw[at+1:]
	}
	sign := ""
	if strings.HasPrefix(mantissa, "-") {
		sign, mantissa = "-", mantissa[1:]
	}
	exponent := new(big.Int)
	if exponentText != "" {
		if _, ok := exponent.SetString(exponentText, 10); !ok {
			return "", fmt.Errorf("invalid JSON exponent %q", exponentText)
		}
	}
	fraction := 0
	if dot := strings.IndexByte(mantissa, '.'); dot >= 0 {
		fraction = len(mantissa) - dot - 1
	}
	digits := strings.TrimLeft(strings.ReplaceAll(mantissa, ".", ""), "0")
	if digits == "" {
		return json.Number("0"), nil
	}
	exponent.Add(exponent, big.NewInt(int64(len(digits)-1-fraction)))
	digits = strings.TrimRight(digits, "0")
	significand := digits[:1]
	if len(digits) > 1 {
		significand += "." + digits[1:]
	}
	if exponent.Sign() == 0 {
		return json.Number(sign + significand), nil
	}
	return json.Number(sign + significand + "e" + exponent.String()), nil
}
