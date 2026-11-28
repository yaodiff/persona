package exportfile_test

import (
	"testing"

	"github.com/nwverse/persona/server/internal/archguard/domainrules"
)

func TestDomainRules(t *testing.T) {
	domainrules.Check(t)
}
