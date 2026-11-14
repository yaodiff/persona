//go:build unix && !darwin

package osproctest

func isSystemSocket(int) bool { return false }
