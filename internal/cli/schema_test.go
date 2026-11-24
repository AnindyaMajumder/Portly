package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// schema is a decoded JSON schema node.
type schema = map[string]any

// loadSchema reads the published contract.
func loadSchema(t *testing.T) schema {
	t.Helper()
	data, err := os.ReadFile("../../schema/portly.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var root schema
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	return root
}

// checkDocument fails unless the output is exactly one document matching the schema.
func checkDocument(t *testing.T, output string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(output))
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, output)
	}
	if decoder.More() {
		t.Fatalf("more than one document:\n%s", output)
	}
	root := loadSchema(t)
	for _, problem := range validate(root, root, value, "$") {
		t.Error(problem)
	}
	return value
}

// validate covers the schema keywords the contract uses. Unknown fields are problems.
func validate(root, node schema, value any, path string) []string {
	if ref, ok := node["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/$defs/")
		return validate(root, root["$defs"].(schema)[name].(schema), value, path)
	}
	if options, ok := node["anyOf"].([]any); ok {
		var problems []string
		for _, option := range options {
			found := validate(root, option.(schema), value, path)
			if len(found) == 0 {
				return nil
			}
			problems = append(problems, found...)
		}
		return problems
	}
	problems := validateScalar(node, value, path)
	switch value := value.(type) {
	case map[string]any:
		problems = append(problems, validateObject(root, node, value, path)...)
	case []any:
		items, _ := node["items"].(schema)
		for i, item := range value {
			problems = append(problems, validate(root, items, item, fmt.Sprintf("%s[%d]", path, i))...)
		}
	}
	return problems
}

// validateObject checks required and known properties.
func validateObject(root, node schema, value map[string]any, path string) []string {
	var problems []string
	required, _ := node["required"].([]any)
	for _, name := range required {
		if _, ok := value[name.(string)]; !ok {
			problems = append(problems, path+": missing "+name.(string))
		}
	}
	properties, _ := node["properties"].(schema)
	for name, item := range value {
		property, ok := properties[name].(schema)
		if !ok {
			problems = append(problems, path+": unknown field "+name)
			continue
		}
		problems = append(problems, validate(root, property, item, path+"."+name)...)
	}
	return problems
}

// validateScalar checks type, const, enum, pattern and range.
func validateScalar(node schema, value any, path string) []string {
	var problems []string
	if wanted, ok := node["type"]; ok && !matchesType(wanted, value) {
		problems = append(problems, fmt.Sprintf("%s: %v is not %v", path, value, wanted))
	}
	if constant, ok := node["const"]; ok && constant != value {
		problems = append(problems, fmt.Sprintf("%s: %v is not %v", path, value, constant))
	}
	if options, ok := node["enum"].([]any); ok && !slices.Contains(options, value) {
		problems = append(problems, fmt.Sprintf("%s: %v is not in the enum", path, value))
	}
	if text, ok := value.(string); ok && node["pattern"] != nil && !regexp.MustCompile(node["pattern"].(string)).MatchString(text) {
		problems = append(problems, path+": pattern")
	}
	if number, ok := value.(float64); ok {
		low, hasLow := node["minimum"].(float64)
		high, hasHigh := node["maximum"].(float64)
		if hasLow && number < low || hasHigh && number > high {
			problems = append(problems, path+": range")
		}
	}
	return problems
}

// matchesType accepts one type or a list of types.
func matchesType(wanted, value any) bool {
	if names, ok := wanted.([]any); ok {
		return slices.ContainsFunc(names, func(name any) bool { return matchesType(name, value) })
	}
	number, isNumber := value.(float64)
	switch wanted {
	case "null":
		return value == nil
	case "integer":
		return isNumber && number == float64(int64(number))
	case "string", "boolean":
		return value != nil && reflect.TypeOf(value).String() == map[any]string{"string": "string", "boolean": "bool"}[wanted]
	case "array":
		_, ok := value.([]any)
		return ok
	}
	_, ok := value.(map[string]any)
	return ok
}

// TestValidatorCatchesProblems proves the schema check can fail.
func TestValidatorCatchesProblems(t *testing.T) {
	root := loadSchema(t)
	for name, text := range map[string]string{
		"missing":   `{"schema_version":"1","command":"list","error":null}`,
		"const":     `{"schema_version":"2","command":"list","error":null,"warnings":[]}`,
		"enum":      `{"schema_version":"1","command":"x","error":null,"warnings":[]}`,
		"unknown":   `{"schema_version":"1","command":"list","error":null,"warnings":[],"extra":1}`,
		"item type": `{"schema_version":"1","command":"list","error":null,"warnings":[1]}`,
		"pattern":   `{"schema_version":"1","command":"stop","error":null,"warnings":[],"plan_id":"xyz"}`,
		"range":     `{"schema_version":"1","command":"list","error":null,"warnings":[],"hidden_udp":-1}`,
		"any of":    `{"schema_version":"1","command":"list","error":{"code":"nope"},"warnings":[]}`,
		"integer":   `{"schema_version":"1","command":"list","error":null,"warnings":[],"hidden_udp":1.5}`,
	} {
		var value any
		if err := json.Unmarshal([]byte(text), &value); err != nil {
			t.Fatal(err)
		}
		if len(validate(root, root, value, "$")) == 0 {
			t.Errorf("%s was accepted", name)
		}
	}
}
