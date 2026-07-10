package unio

import (
	"slices"
	"sort"
	"testing"

	"github.com/Fullstop000/unio/internal/supportmatrix"
)

func TestEveryRegisteredAgentHasSupportProfile(t *testing.T) {
	registered := make([]string, 0, len(driverFactories))
	for kind := range driverFactories {
		registered = append(registered, string(kind))
	}
	sort.Strings(registered)
	if profiles := supportmatrix.ProfileKinds(); !slices.Equal(registered, profiles) {
		t.Fatalf("registered agents = %v; support profiles = %v", registered, profiles)
	}
}
