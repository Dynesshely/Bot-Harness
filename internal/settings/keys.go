package settings

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

// ConfigKeyManifest is the backend's configuration surface, rendered for
// front-end and CI drift checks. Every name is a JSON key the admin API accepts,
// never a value.
type ConfigKeyManifest struct {
	// Settings is every field accepted by PUT /api/config and settings_write.
	Settings []string `json:"settings"`
	// GroupOverride is every field of one group_overrides entry.
	GroupOverride []string `json:"group_override"`
}

// ConfigKeys builds the manifest from the live schema: KnownKeys for Settings,
// and the struct tags for GroupOverride, so neither list can be hand-edited out
// of sync with the types.
func ConfigKeys() ConfigKeyManifest {
	return ConfigKeyManifest{
		Settings:      KnownKeys(),
		GroupOverride: GroupOverrideKeys(),
	}
}

// GroupOverrideKeys lists the JSON field names of GroupOverride.
func GroupOverrideKeys() []string {
	t := reflect.TypeOf(GroupOverride{})
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ConfigKeysJSON renders the manifest with stable formatting (two-space indent,
// trailing newline) so it can be diffed and committed as a golden file.
func ConfigKeysJSON() ([]byte, error) {
	raw, err := json.MarshalIndent(ConfigKeys(), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}
