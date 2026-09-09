package tui

import "testing"

// The palette keeps two invariants on BOTH backgrounds: priority and status
// families are disjoint (a colored chip is never ambiguous), and every entry
// inside a family is distinct from its siblings.

var priorityFamily = []string{priorityP0, priorityP1, priorityP2, priorityP3, priorityP4}

var statusFamily = []string{statusOpen, statusInProgress, statusBlocked, statusDeferred, statusClosed, statusHold, statusHooked}

func TestLightPaletteCoversEveryFamilyColor(t *testing.T) {
	for _, code := range append(append([]string(nil), priorityFamily...), statusFamily...) {
		if light, ok := lightPalette[code]; ok && light == "" {
			t.Errorf("lightPalette[%s] is empty", code)
		}
	}
	// Grays and dims may be absent (shared across backgrounds); everything
	// else in the families must have a light variant.
	for _, code := range []string{priorityP0, priorityP1, priorityP2, priorityP3, statusOpen, statusInProgress, statusBlocked, statusDeferred, statusHold, statusHooked} {
		if _, ok := lightPalette[code]; !ok {
			t.Errorf("family color %s has no light variant", code)
		}
	}
}

func resolverFor(bg string) func(string) string {
	if bg == "dark" {
		return func(code string) string { return code }
	}
	return func(code string) string {
		if light, ok := lightPalette[code]; ok {
			return light
		}
		return code
	}
}

func TestPaletteFamiliesAreDisjointPerBackground(t *testing.T) {
	for _, bg := range []string{"dark", "light"} {
		resolve := resolverFor(bg)
		seen := map[string]string{}
		for _, code := range priorityFamily {
			resolved := resolve(code)
			if owner, clash := seen[resolved]; clash {
				t.Errorf("%s background: priority color %s clashes with %s", bg, resolved, owner)
			}
			seen[resolved] = "priority:" + code
		}
		for _, code := range statusFamily {
			resolved := resolve(code)
			if owner, clash := seen[resolved]; clash {
				t.Errorf("%s background: status color %s clashes with %s", bg, resolved, owner)
			}
			seen[resolved] = "status:" + code
		}
	}
}

func TestPaletteDistinctWithinEachFamilyPerBackground(t *testing.T) {
	for _, bg := range []string{"dark", "light"} {
		resolve := resolverFor(bg)
		for _, family := range []struct {
			name  string
			codes []string
		}{
			{"priority", priorityFamily},
			{"status", statusFamily},
		} {
			seen := map[string]bool{}
			for _, code := range family.codes {
				resolved := resolve(code)
				if seen[resolved] {
					t.Errorf("%s background: %s family repeats color %s", bg, family.name, resolved)
				}
				seen[resolved] = true
			}
		}
	}
}

func TestPaletteColorKeepsDarkCodeAsFallback(t *testing.T) {
	// A code without a light variant still renders (its dark code) on both
	// backgrounds; the adaptive wrapper never produces an empty color.
	color := paletteColor(statusClosed)
	if color.Dark != statusClosed {
		t.Errorf("dark side = %q, want %q", color.Dark, statusClosed)
	}
}
