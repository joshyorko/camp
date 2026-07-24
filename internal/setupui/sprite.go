package setupui

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
)

// Sprite is a rectangular block of terminal-cell art. It is the exact schema
// authored and visually reviewed with tools/ansishot/spritecheck.py, so the
// checked-in JSON assets are the single source of truth for landmark artwork —
// there is no Python-only art. Glyphs holds what to draw ('space' is
// transparent) and Colors holds one legend key per cell; Legend maps a key to
// a Palette color name.
type Sprite struct {
	Name   string            `json:"name"`
	Legend map[string]string `json:"legend"`
	Glyphs []string          `json:"glyphs"`
	Colors []string          `json:"colors"`
}

// Width returns the widest glyph row in cells.
func (s Sprite) Width() int {
	w := 0
	for _, row := range s.Glyphs {
		if n := len([]rune(row)); n > w {
			w = n
		}
	}
	return w
}

// Height returns the number of glyph rows.
func (s Sprite) Height() int { return len(s.Glyphs) }

//go:embed assets/sprites/*.json
var spriteFS embed.FS

// LoadSprites reads every embedded sprite asset, keyed by sprite name.
func LoadSprites() (map[string]Sprite, error) {
	entries, err := spriteFS.ReadDir("assets/sprites")
	if err != nil {
		return nil, fmt.Errorf("read sprite assets: %w", err)
	}
	out := make(map[string]Sprite, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := fs.ReadFile(spriteFS, "assets/sprites/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("read sprite %s: %w", e.Name(), err)
		}
		var s Sprite
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("parse sprite %s: %w", e.Name(), err)
		}
		if s.Name == "" {
			s.Name = strings.TrimSuffix(e.Name(), ".json")
		}
		out[s.Name] = s
	}
	return out, nil
}
