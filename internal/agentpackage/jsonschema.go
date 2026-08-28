package agentpackage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ValidateJSON は Agent Package が所有する Draft 2020-12 schema の、Kudo が採用する
// closed subset に対して document を検証する。未知 keyword を汎用実装として推測せず、
// package fixture と runtime payload の双方へ同じ validator を使う。
func ValidateJSON(schemaBytes, documentBytes []byte) error {
	if err := rejectDuplicateJSONNames(schemaBytes); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	if err := rejectDuplicateJSONNames(documentBytes); err != nil {
		return fmt.Errorf("document: %w", err)
	}
	var schema any
	if err := decodeJSONValue(schemaBytes, &schema); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	var document any
	if err := decodeJSONValue(documentBytes, &document); err != nil {
		return fmt.Errorf("document: %w", err)
	}
	root, ok := schema.(map[string]any)
	if !ok {
		return errors.New("schema root が object でない")
	}
	if err := validateSchemaNode(root, root, document, "$", map[string]bool{}); err != nil {
		return err
	}
	return nil
}

func decodeJSONValue(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("JSON document の後ろに余分な値がある")
	}
	return nil
}

func validateSchemaNode(root, schema map[string]any, value any, location string, resolving map[string]bool) error {
	if ref, ok := schema["$ref"]; ok {
		refString, ok := ref.(string)
		if !ok || !strings.HasPrefix(refString, "#/$defs/") || resolving[refString] {
			return fmt.Errorf("%s: 未対応または循環する $ref: %v", location, ref)
		}
		definition, err := resolveDefinition(root, strings.TrimPrefix(refString, "#/$defs/"))
		if err != nil {
			return fmt.Errorf("%s: %w", location, err)
		}
		resolving[refString] = true
		err = validateSchemaNode(root, definition, value, location, resolving)
		delete(resolving, refString)
		return err
	}
	if alternatives, ok := schema["oneOf"]; ok {
		items, ok := alternatives.([]any)
		if !ok || len(items) == 0 {
			return fmt.Errorf("%s: oneOf が不正", location)
		}
		matches := 0
		for _, item := range items {
			candidate, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("%s: oneOf item が object でない", location)
			}
			if validateSchemaNode(root, candidate, value, location, cloneResolving(resolving)) == nil {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("%s: oneOf の一致数が %d", location, matches)
		}
	}
	if expected, ok := schema["type"]; ok && !matchesJSONType(expected, value) {
		return fmt.Errorf("%s: type が %v でない", location, expected)
	}
	if constant, ok := schema["const"]; ok && !reflect.DeepEqual(constant, value) {
		return fmt.Errorf("%s: const と一致しない", location)
	}
	if enum, ok := schema["enum"]; ok {
		values, ok := enum.([]any)
		if !ok {
			return fmt.Errorf("%s: enum が array でない", location)
		}
		matched := false
		for _, candidate := range values {
			matched = matched || reflect.DeepEqual(candidate, value)
		}
		if !matched {
			return fmt.Errorf("%s: enum に含まれない", location)
		}
	}

	switch typed := value.(type) {
	case map[string]any:
		if err := validateObject(root, schema, typed, location, resolving); err != nil {
			return err
		}
	case []any:
		if err := validateArray(root, schema, typed, location, resolving); err != nil {
			return err
		}
	case string:
		if err := validateString(schema, typed, location); err != nil {
			return err
		}
	}
	return nil
}

func validateObject(root, schema, value map[string]any, location string, resolving map[string]bool) error {
	properties, _ := schema["properties"].(map[string]any)
	if required, ok := schema["required"]; ok {
		list, ok := required.([]any)
		if !ok {
			return fmt.Errorf("%s: required が array でない", location)
		}
		for _, nameValue := range list {
			name, ok := nameValue.(string)
			if !ok {
				return fmt.Errorf("%s: required name が文字列でない", location)
			}
			if _, present := value[name]; !present {
				return fmt.Errorf("%s.%s: required field が無い", location, name)
			}
		}
	}
	additional, hasAdditional := schema["additionalProperties"]
	for name, item := range value {
		propertyValue, known := properties[name]
		if !known {
			if hasAdditional && additional == false {
				return fmt.Errorf("%s.%s: unknown field", location, name)
			}
			continue
		}
		property, ok := propertyValue.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.%s: property schema が object でない", location, name)
		}
		if err := validateSchemaNode(root, property, item, location+"."+name, cloneResolving(resolving)); err != nil {
			return err
		}
	}
	return nil
}

func validateArray(root, schema map[string]any, value []any, location string, resolving map[string]bool) error {
	if minimum, ok := jsonInteger(schema["minItems"]); ok && int64(len(value)) < minimum {
		return fmt.Errorf("%s: item 数が %d 未満", location, minimum)
	}
	if maximum, ok := jsonInteger(schema["maxItems"]); ok && int64(len(value)) > maximum {
		return fmt.Errorf("%s: item 数が %d を超える", location, maximum)
	}
	itemValue, ok := schema["items"]
	if !ok {
		return nil
	}
	itemSchema, ok := itemValue.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: items schema が object でない", location)
	}
	for i, item := range value {
		if err := validateSchemaNode(root, itemSchema, item, fmt.Sprintf("%s[%d]", location, i), cloneResolving(resolving)); err != nil {
			return err
		}
	}
	return nil
}

func validateString(schema map[string]any, value, location string) error {
	length := int64(utf8.RuneCountInString(value))
	if minimum, ok := jsonInteger(schema["minLength"]); ok && length < minimum {
		return fmt.Errorf("%s: 文字数が %d 未満", location, minimum)
	}
	if maximum, ok := jsonInteger(schema["maxLength"]); ok && length > maximum {
		return fmt.Errorf("%s: 文字数が %d を超える", location, maximum)
	}
	if patternValue, ok := schema["pattern"]; ok {
		pattern, ok := patternValue.(string)
		if !ok {
			return fmt.Errorf("%s: pattern が文字列でない", location)
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("%s: pattern が不正: %w", location, err)
		}
		if !compiled.MatchString(value) {
			return fmt.Errorf("%s: pattern と一致しない", location)
		}
	}
	return nil
}

func resolveDefinition(root map[string]any, name string) (map[string]any, error) {
	definitions, ok := root["$defs"].(map[string]any)
	if !ok {
		return nil, errors.New("$defs が無い")
	}
	definition, ok := definitions[name].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("$defs.%s が無い", name)
	}
	return definition, nil
}

func matchesJSONType(expected, value any) bool {
	if list, ok := expected.([]any); ok {
		for _, candidate := range list {
			if matchesJSONType(candidate, value) {
				return true
			}
		}
		return false
	}
	typeName, ok := expected.(string)
	if !ok {
		return false
	}
	switch typeName {
	case "object":
		_, ok = value.(map[string]any)
	case "array":
		_, ok = value.([]any)
	case "string":
		_, ok = value.(string)
	case "boolean":
		_, ok = value.(bool)
	case "number":
		_, ok = value.(json.Number)
	case "integer":
		number, numberOK := value.(json.Number)
		_, err := number.Int64()
		ok = numberOK && err == nil
	case "null":
		ok = value == nil
	default:
		ok = false
	}
	return ok
}

func jsonInteger(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	integer, err := number.Int64()
	return integer, err == nil
}

func cloneResolving(source map[string]bool) map[string]bool {
	clone := make(map[string]bool, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
