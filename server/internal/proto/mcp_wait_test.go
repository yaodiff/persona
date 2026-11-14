package proto

import (
	"strings"
	"testing"
)

func TestValidMCPWaitCallID(t *testing.T) {
	for _, id := range []string{
		"123e4567-e89b-42d3-a456-426614174000",
		"123E4567-E89B-42D3-A456-426614174000",
	} {
		if !ValidMCPWaitCallID(id) {
			t.Errorf("valid UUID %q was rejected", id)
		}
	}
	for _, id := range []string{
		"",
		"not-a-uuid",
		"123e4567-e89b-42d3-a456-42661417400",
		"123e4567-e89b-42d3-a456-4266141740000",
		"123e4567-e89b-42d3-a456-42661417400g",
		"123e4567_e89b-42d3-a456-426614174000",
		"{123e4567-e89b-42d3-a456-426614174000}",
		" 123e4567-e89b-42d3-a456-426614174000",
		strings.Repeat("x", 1<<20),
	} {
		if ValidMCPWaitCallID(id) {
			t.Errorf("invalid call id with length %d was accepted", len(id))
		}
	}
}
