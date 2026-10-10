package config

import (
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// MarshalYAML preserves OAuth scope across watcher/server YAML snapshots. A
// legacy-only Config still produces its original layout. Database field updates
// use LegacyConfigRoot and reconcile any existing OAuth scope separately.
func (cfg Config) MarshalYAML() (any, error) {
	var root yaml.Node
	if err := root.Encode(legacyConfig(cfg)); err != nil {
		return nil, err
	}
	value := reflect.ValueOf(cfg)
	for _, path := range v8Paths {
		if !strings.HasPrefix(path.current, "oauth.providers.") || !cfg.OAuthOnlyFields[path.old] {
			continue
		}
		field := yamlPath(&root, path.old)
		if field == nil {
			field = new(yaml.Node)
			if err := field.Encode(value.FieldByIndex(v8FieldIndexes[path.old]).Interface()); err != nil {
				return nil, err
			}
		}
		copy := deepCopyYAMLNode(field)
		deleteYAMLPath(&root, path.old)
		setYAMLPath(&root, path.current, copy)
	}
	return &root, nil
}
