package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountAvailabilityStateDistinguishesGrokCooldownAndSlow(t *testing.T) {
	tests := []struct {
		name   string
		acc    Account
		flags  [6]bool
		wanted string
	}{
		{name: "429 cooldown", acc: Account{TempUnschedulableReason: "grok free usage exhausted"}, flags: [6]bool{false, false, false, true, false, false}, wanted: "cooldown_429"},
		{name: "403 cooldown", acc: Account{TempUnschedulableReason: "grok entitlement or subscription tier denied"}, flags: [6]bool{false, false, false, true, false, false}, wanted: "cooldown_403"},
		{name: "slow", flags: [6]bool{true, false, false, false, true, false}, wanted: "slow"},
		{name: "available", flags: [6]bool{true, false, false, false, false, false}, wanted: "available"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := accountAvailabilityState(tt.acc, tt.flags[0], tt.flags[1], tt.flags[2], tt.flags[3], tt.flags[4], tt.flags[5])
			require.Equal(t, tt.wanted, got)
		})
	}
}
