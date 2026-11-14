//go:build !darwin

package node

// claudeAppAttendedNow: only macOS's front application and idle time are read, so elsewhere (Windows, whose index is
// read: claudeDesktopDirs) a session the app shows counts as attended, and only one it does not show can be unread.
func claudeAppAttendedNow() bool { return true }
