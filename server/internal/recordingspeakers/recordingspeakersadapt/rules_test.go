package recordingspeakersadapt_test

import (
	"testing"

	"github.com/nwverse/persona/server/internal/archguard/domainrules"
)

// The domain rules for this package and the code that imports it (docs/server/architecture.md "Domain model").
func TestDomainRules(t *testing.T) { domainrules.Check(t) }
