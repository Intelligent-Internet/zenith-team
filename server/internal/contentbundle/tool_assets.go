package contentbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// ToolAsset is generated from a checksum-verified npm package at build time.
// It is embedded in the binary, never committed to either repository.
type ToolAsset struct {
	ToolKey string `json:"toolKey"`
	License string `json:"license"`
	Skill   Skill  `json:"skill"`
}

type toolAssets struct {
	ReleaseHash string      `json:"releaseHash"`
	Assets      []ToolAsset `json:"assets"`
}

// EncodeToolAssets binds verified downloaded content to the pinned metadata.
func EncodeToolAssets(catalog ManagedTools, assets []ToolAsset) ([]byte, error) {
	hash, err := managedToolsHash(catalog)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(toolAssets{ReleaseHash: hash, Assets: assets})
	if err != nil {
		return nil, err
	}
	if _, err := DecodeToolAssets(catalog, raw); err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// DecodeToolAssets rejects stale generated assets and verifies all skill bytes.
func DecodeToolAssets(catalog ManagedTools, raw []byte) ([]ToolAsset, error) {
	var document toolAssets
	if err := DecodeJSON(raw, &document); err != nil {
		return nil, err
	}
	hash, err := managedToolsHash(catalog)
	if err != nil {
		return nil, err
	}
	if document.ReleaseHash != hash {
		return nil, errors.New("generated Tool assets are stale; run make tools-prepare")
	}
	expected := map[string]SkillRef{}
	for _, release := range catalog.Releases {
		if release.Skill.ID != "" {
			expected[release.Key] = release.Skill
		}
	}
	for _, asset := range document.Assets {
		ref, ok := expected[asset.ToolKey]
		if !ok || asset.License == "" {
			return nil, errors.New("unexpected Tool asset or missing license")
		}
		if err := ValidateSkill(ref, asset.Skill); err != nil {
			return nil, fmt.Errorf("generated Tool %s: %w", asset.ToolKey, err)
		}
		delete(expected, asset.ToolKey)
	}
	if len(expected) != 0 {
		return nil, errors.New("generated Tool assets are incomplete; run make tools-prepare")
	}
	return document.Assets, nil
}

func managedToolsHash(catalog ManagedTools) (string, error) {
	raw, err := json.Marshal(catalog)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}
