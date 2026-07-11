package inference

import (
	"encoding/json"
	"strings"
)

type singleEnumObjectSchema struct {
	Type                 string                        `json:"type"`
	AdditionalProperties *bool                         `json:"additionalProperties"`
	Properties           map[string]singleEnumProperty `json:"properties"`
	Required             []string                      `json:"required"`
}

type singleEnumProperty struct {
	Type string   `json:"type"`
	Enum []string `json:"enum"`
}

func normalizeBareEnumObjectOutput(schemaJSON string, output string) (string, bool) {
	var schema singleEnumObjectSchema
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		return "", false
	}
	if schema.Type != "object" || schema.AdditionalProperties == nil ||
		*schema.AdditionalProperties || len(schema.Properties) != 1 || len(schema.Required) != 1 {
		return "", false
	}
	propertyName := schema.Required[0]
	property, found := schema.Properties[propertyName]
	if !found || property.Type != "string" || len(property.Enum) == 0 {
		return "", false
	}
	for _, enumValue := range property.Enum {
		if !strings.EqualFold(output, enumValue) {
			continue
		}
		normalized, err := json.Marshal(map[string]string{propertyName: enumValue})
		if err != nil {
			return "", false
		}
		return string(normalized), true
	}
	return "", false
}
