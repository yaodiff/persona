package cliwire

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrorKind is the stable category of an lark-cli failure. The connector maps these
// categories to the generic connector states without exposing command output.
type ErrorKind string

const (
	KindNotInstalled       ErrorKind = "not_installed"
	KindNotLoggedIn        ErrorKind = "not_logged_in"
	KindExpired            ErrorKind = "expired"
	KindMissingPermission  ErrorKind = "missing_permission"
	KindBotNotInChat       ErrorKind = "bot_not_in_chat"
	KindRateLimited        ErrorKind = "rate_limited"
	KindUnsupportedVersion ErrorKind = "unsupported_version"
	KindTimeout            ErrorKind = "timeout"
	KindOther              ErrorKind = "other"
)

// Short aliases make category assertions convenient for callers and tests.
const (
	NotInstalled       = KindNotInstalled
	NotLoggedIn        = KindNotLoggedIn
	Expired            = KindExpired
	MissingPermission  = KindMissingPermission
	RateLimited        = KindRateLimited
	UnsupportedVersion = KindUnsupportedVersion
	Timeout            = KindTimeout
	Other              = KindOther
)

// Error is a safe lark-cli error. Raw stdout and stderr are deliberately kept out
// of this value: CLI output can contain message bodies, account data, or tokens.
type Error struct {
	Kind          ErrorKind
	MissingScopes []string
	RetryAfter    time.Duration
	Version       string
	Minimum       string
	cause         error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	switch e.Kind {
	case KindNotInstalled:
		return "lark-cli is not installed"
	case KindNotLoggedIn:
		return "lark-cli is not signed in"
	case KindExpired:
		return "lark-cli login has expired"
	case KindMissingPermission:
		if len(e.MissingScopes) > 0 {
			scopes := append([]string(nil), e.MissingScopes...)
			sort.Strings(scopes)
			return "lark-cli is missing permission: " + strings.Join(scopes, ", ")
		}
		return "lark-cli is missing permission"
	case KindBotNotInChat:
		return "the bot is not in this chat; add the bot to the group before sending; no alternative sender was tried"
	case KindRateLimited:
		return "lark-cli is rate limited"
	case KindUnsupportedVersion:
		return fmt.Sprintf("lark-cli version %s is older than the supported minimum %s", e.Version, e.Minimum)
	case KindTimeout:
		return "lark-cli timed out"
	default:
		return "lark-cli command failed"
	}
}

func (e *Error) Unwrap() error { return e.cause }

// KindOf returns the category of err, or KindOther for an unknown error.
func KindOf(err error) ErrorKind {
	var e *Error
	if errors.As(err, &e) && e != nil {
		return e.Kind
	}
	return KindOther
}

// NewError is an error of kind with its cause.
func NewError(kind ErrorKind, cause error) *Error { return &Error{Kind: kind, cause: cause} }
