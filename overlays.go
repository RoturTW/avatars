package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Overlay struct {
	Name     string
	Requires string
	Size     [2]int
	Offset   [2]int
}

func loadOverlays() []Overlay {
	overlaysPath := filepath.Join("./overlays", "-manifest.json")

	_, err := os.Stat(overlaysPath)
	if err != nil {
		return nil
	}

	overlays, err := os.ReadFile(overlaysPath)
	if err != nil {
		return nil
	}

	var overlaysData []Overlay
	err = json.Unmarshal(overlays, &overlaysData)
	if err != nil {
		return nil
	}
	return overlaysData
}
