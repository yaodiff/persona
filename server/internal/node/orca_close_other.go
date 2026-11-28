//go:build !darwin && !linux

package node

import "context"

func orcaShellMatchesTerminal(int, string) bool { return false }
func orcaCloseForeground(int) bool              { return false }
func orcaCloseCwd(context.Context, int) string  { return "" }
