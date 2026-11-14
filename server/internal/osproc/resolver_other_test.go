//go:build unix && !darwin

package osproc

import "testing"

func reliableResolverWarmup() {}

func instantResolverWarmup(*testing.T) {}
