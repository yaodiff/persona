package screenlink

import "strings"

// firewallVerdict reads socketfilterfw's answers: an enabled firewall blocks unless this program is explicitly
// permitted and not everything incoming is blocked. An answer it can't read counts as blocking: listening could make
// macOS ask on the Mac's screen, which no one may be there to answer.
func firewallVerdict(global, blockAll, app string) bool {
	if strings.Contains(global, "disabled") || strings.Contains(global, "State = 0") {
		return false
	}
	if !strings.Contains(global, "enabled") && !strings.Contains(global, "State = 1") && !strings.Contains(global, "State = 2") {
		return true
	}
	if strings.Contains(blockAll, "enabled") || strings.Contains(blockAll, "ENABLED") {
		return true
	}
	return !strings.Contains(app, "is permitted")
}
