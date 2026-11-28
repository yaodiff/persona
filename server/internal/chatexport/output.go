package chatexport

import (
	"context"
	"errors"

	"github.com/nwverse/persona/server/internal/exportfile"
	"github.com/nwverse/persona/server/internal/i18n"
)

func exportError(lang i18n.Lang, err error) error {
	switch {
	case errors.Is(err, exportfile.ErrExists):
		return errors.New(i18n.T(lang, "chatexport.exists"))
	case errors.Is(err, context.Canceled):
		return errors.New(i18n.T(lang, "chatexport.canceled"))
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New(i18n.T(lang, "chatexport.timed_out"))
	default:
		return errors.New(i18n.T(lang, "chatexport.output_failed"))
	}
}
