package tui

import (
	"os"
	"path/filepath"
	"strings"
)

// KeyMap contains configurable key bindings. Values use Bubble Tea's key
// names (for example "ctrl+d", "shift+tab", or a single rune).
type KeyMap map[string]string

var defaultKeys = KeyMap{
	"quit": "q", "refresh": "r", "help": "?", "filter": "f", "sort": "s",
	"graph": "v", "next_view": "tab", "prev_view": "shift+tab",
}

func loadKeyMap() KeyMap {
	keys := make(KeyMap, len(defaultKeys))
	for action, key := range defaultKeys {
		keys[action] = key
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return keys
	}
	contents, err := os.ReadFile(filepath.Join(home, ".config", "beads-tui", "config.toml"))
	if err != nil {
		return keys
	}
	return parseKeyBindings(string(contents), keys)
}

func parseKeyBindings(contents string, keys KeyMap) KeyMap {
	parsed := make(KeyMap, len(keys))
	for action, key := range keys {
		parsed[action] = key
	}
	section := ""
	for _, rawLine := range strings.Split(contents, "\n") {
		line := strings.TrimSpace(strings.SplitN(rawLine, "#", 2)[0])
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		if section != "keybindings" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		action := strings.TrimSpace(parts[0])
		key := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
		if _, ok := parsed[action]; ok && key != "" {
			parsed[action] = key
		}
	}
	return parsed
}

func (k KeyMap) is(action, pressed string) bool { return k[action] == pressed }
