package v1alpha1

import "testing"

func TestIsReservedName(t *testing.T) {
	reserved := []string{
		"sympozium",
		"sympozium-skills",
		"sympozium_skills",
		"SYMPOZIUM-SKILLS",
		"Sympozium-Anything",
		"  sympozium-skills  ",
		// No separator required: the prefix is the reservation, so a name that
		// merely starts with it cannot squat near an internal one either.
		"sympoziumskills",
	}
	for _, name := range reserved {
		if !IsReservedName(name) {
			t.Errorf("IsReservedName(%q) = false, want true", name)
		}
	}

	allowed := []string{
		"github",
		"grafana-mcp",
		"my-sympozium-server", // the prefix reserves the start, not the substring
		"symp",
		"",
	}
	for _, name := range allowed {
		if IsReservedName(name) {
			t.Errorf("IsReservedName(%q) = true, want false — this is an operator's to use", name)
		}
	}
}
