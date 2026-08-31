package tui

import "testing"

func TestParseKeyBindingsOverridesKnownActionsOnly(t *testing.T) {
	keys := parseKeyBindings("[keybindings]\nfilter = \"/\"\nnext_view = 'n'\nunknown = \"x\"\n[other]\nquit = \"z\"\n", defaultKeys)
	if keys["filter"] != "/" || keys["next_view"] != "n" {
		t.Fatalf("overrides not applied: %+v", keys)
	}
	if keys["quit"] != defaultKeys["quit"] {
		t.Errorf("other section changed quit to %q", keys["quit"])
	}
	if _, ok := keys["unknown"]; ok {
		t.Error("unknown action should be ignored")
	}
}
