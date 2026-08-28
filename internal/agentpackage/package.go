// Package agentpackage は provider 非依存の Agent Package closure を読み、content identity を検証する。
package agentpackage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/mrbaron3/kudo/internal/contract"
)

const (
	ManifestFile                      = "agent-package.json"
	ToolProfileSchemaV1Alpha1         = "kudo.agent-tool-profile/v1alpha1"
	NetworkNone               Network = "none"
	maxComponentBytes                 = 4 << 20
)

type Network string

type FileDescriptor struct {
	Path      string          `json:"path"`
	MediaType string          `json:"mediaType"`
	Digest    contract.Digest `json:"digest"`
}

type FixtureDescriptor struct {
	Name   string         `json:"name"`
	Input  FileDescriptor `json:"input"`
	Output FileDescriptor `json:"output"`
}

// Manifest は Agent Package の全 provider 非依存 component を content digest で閉じる。
// provider、model、credential、session、local path は Execution Policy と launcher の責務である。
type Manifest struct {
	Schema       string              `json:"schema"`
	Name         string              `json:"name"`
	Version      string              `json:"version"`
	Operation    string              `json:"operation"`
	Instructions FileDescriptor      `json:"instructions"`
	InputSchema  FileDescriptor      `json:"inputSchema"`
	OutputSchema FileDescriptor      `json:"outputSchema"`
	ToolProfile  FileDescriptor      `json:"toolProfile"`
	Fixtures     []FixtureDescriptor `json:"fixtures"`
}

type ToolProfile struct {
	Schema       string   `json:"schema"`
	Capabilities []string `json:"capabilities"`
	Network      Network  `json:"network"`
}

type Fixture struct {
	Name   string
	Input  []byte
	Output []byte
}

// Package は検証済み manifest closure である。各 byte slice は loader が所有する。
type Package struct {
	Manifest     Manifest
	Ref          contract.AgentPackageRef
	Instructions []byte
	InputSchema  []byte
	OutputSchema []byte
	ToolProfile  ToolProfile
	Fixtures     []Fixture

	toolProfileBytes []byte
}

// Load は root/agent-package.json と全 component を読み、digest と schema を照合する。
func Load(fsys fs.FS, root string) (Package, error) {
	if fsys == nil {
		return Package{}, errors.New("Agent Package filesystem は必須")
	}
	if root != "." && !validRelativePath(root) {
		return Package{}, fmt.Errorf("Agent Package root が不正: %q", root)
	}
	manifestBytes, err := readBounded(fsys, path.Join(root, ManifestFile))
	if err != nil {
		return Package{}, fmt.Errorf("Agent Package manifest: %w", err)
	}
	var manifest Manifest
	if err := decodeStrictJSON(manifestBytes, &manifest); err != nil {
		return Package{}, fmt.Errorf("Agent Package manifest: %w", err)
	}
	if err := validateManifest(manifest); err != nil {
		return Package{}, err
	}

	read := func(label string, descriptor FileDescriptor) ([]byte, error) {
		data, err := readBounded(fsys, path.Join(root, descriptor.Path))
		if err != nil {
			return nil, fmt.Errorf("%s %q: %w", label, descriptor.Path, err)
		}
		if got := contract.SHA256(data); got != descriptor.Digest {
			return nil, fmt.Errorf("%s %q の digest が一致しない: got %s, want %s",
				label, descriptor.Path, got, descriptor.Digest)
		}
		return data, nil
	}

	instructions, err := read("instructions", manifest.Instructions)
	if err != nil {
		return Package{}, err
	}
	if !utf8.Valid(instructions) || strings.TrimSpace(string(instructions)) == "" || bytes.IndexByte(instructions, 0) >= 0 {
		return Package{}, errors.New("instructions は空でない UTF-8 text でなければならない")
	}
	inputSchema, err := read("input schema", manifest.InputSchema)
	if err != nil {
		return Package{}, err
	}
	outputSchema, err := read("output schema", manifest.OutputSchema)
	if err != nil {
		return Package{}, err
	}
	if err := validateSchemaDocument(inputSchema, "kudo.agent-input/"+manifest.Name+"/"); err != nil {
		return Package{}, fmt.Errorf("input schema: %w", err)
	}
	if err := validateSchemaDocument(outputSchema, "kudo.agent-output/"+manifest.Name+"/"); err != nil {
		return Package{}, fmt.Errorf("output schema: %w", err)
	}
	profileBytes, err := read("tool profile", manifest.ToolProfile)
	if err != nil {
		return Package{}, err
	}
	var profile ToolProfile
	if err := decodeStrictJSON(profileBytes, &profile); err != nil {
		return Package{}, fmt.Errorf("tool profile: %w", err)
	}
	if err := validateToolProfile(manifest.Operation, profile); err != nil {
		return Package{}, err
	}

	fixtures := make([]Fixture, 0, len(manifest.Fixtures))
	for _, descriptor := range manifest.Fixtures {
		input, err := read("fixture input", descriptor.Input)
		if err != nil {
			return Package{}, err
		}
		if err := ValidateJSON(inputSchema, input); err != nil {
			return Package{}, fmt.Errorf("fixture %q input: %w", descriptor.Name, err)
		}
		output, err := read("fixture output", descriptor.Output)
		if err != nil {
			return Package{}, err
		}
		if err := ValidateJSON(outputSchema, output); err != nil {
			return Package{}, fmt.Errorf("fixture %q output: %w", descriptor.Name, err)
		}
		fixtures = append(fixtures, Fixture{Name: descriptor.Name, Input: input, Output: output})
	}

	identity, err := canonicalManifest(manifest)
	if err != nil {
		return Package{}, err
	}
	pkg := Package{
		Manifest:         cloneManifest(manifest),
		Ref:              contract.AgentPackageRef{Schema: manifest.Schema, Digest: contract.SHA256(identity)},
		Instructions:     append([]byte(nil), instructions...),
		InputSchema:      append([]byte(nil), inputSchema...),
		OutputSchema:     append([]byte(nil), outputSchema...),
		ToolProfile:      cloneToolProfile(profile),
		Fixtures:         cloneFixtures(fixtures),
		toolProfileBytes: append([]byte(nil), profileBytes...),
	}
	if err := Validate(pkg); err != nil {
		return Package{}, err
	}
	return pkg, nil
}

// Validate は load 後の Package value が manifest closure と一致したままかを再検証する。
// Package は worker 境界を値で横断するため、provider 起動直前にも呼び出す。
func Validate(pkg Package) error {
	if err := validateManifest(pkg.Manifest); err != nil {
		return err
	}
	identity, err := canonicalManifest(pkg.Manifest)
	if err != nil {
		return err
	}
	wantRef := contract.AgentPackageRef{Schema: pkg.Manifest.Schema, Digest: contract.SHA256(identity)}
	if pkg.Ref != wantRef {
		return fmt.Errorf("Agent Package ref が manifest と一致しない: got %#v, want %#v", pkg.Ref, wantRef)
	}
	components := []struct {
		name       string
		descriptor FileDescriptor
		data       []byte
	}{
		{"instructions", pkg.Manifest.Instructions, pkg.Instructions},
		{"input schema", pkg.Manifest.InputSchema, pkg.InputSchema},
		{"output schema", pkg.Manifest.OutputSchema, pkg.OutputSchema},
		{"tool profile", pkg.Manifest.ToolProfile, pkg.toolProfileBytes},
	}
	for _, component := range components {
		if contract.SHA256(component.data) != component.descriptor.Digest {
			return fmt.Errorf("%s bytes が manifest digest と一致しない", component.name)
		}
	}
	if !utf8.Valid(pkg.Instructions) || strings.TrimSpace(string(pkg.Instructions)) == "" || bytes.IndexByte(pkg.Instructions, 0) >= 0 {
		return errors.New("instructions は空でない UTF-8 text でなければならない")
	}
	if err := validateSchemaDocument(pkg.InputSchema, "kudo.agent-input/"+pkg.Manifest.Name+"/"); err != nil {
		return fmt.Errorf("input schema: %w", err)
	}
	if err := validateSchemaDocument(pkg.OutputSchema, "kudo.agent-output/"+pkg.Manifest.Name+"/"); err != nil {
		return fmt.Errorf("output schema: %w", err)
	}
	var storedProfile ToolProfile
	if err := decodeStrictJSON(pkg.toolProfileBytes, &storedProfile); err != nil {
		return fmt.Errorf("tool profile: %w", err)
	}
	if storedProfile.Schema != pkg.ToolProfile.Schema || storedProfile.Network != pkg.ToolProfile.Network ||
		!slices.Equal(storedProfile.Capabilities, pkg.ToolProfile.Capabilities) {
		return errors.New("tool profile value が component bytes と一致しない")
	}
	if err := validateToolProfile(pkg.Manifest.Operation, pkg.ToolProfile); err != nil {
		return err
	}
	fixtureByName := make(map[string]Fixture, len(pkg.Fixtures))
	for _, fixture := range pkg.Fixtures {
		if _, exists := fixtureByName[fixture.Name]; exists {
			return fmt.Errorf("fixture value が重複: %s", fixture.Name)
		}
		fixtureByName[fixture.Name] = fixture
	}
	if len(fixtureByName) != len(pkg.Manifest.Fixtures) {
		return errors.New("fixture value の件数が manifest と一致しない")
	}
	for _, descriptor := range pkg.Manifest.Fixtures {
		fixture, ok := fixtureByName[descriptor.Name]
		if !ok || contract.SHA256(fixture.Input) != descriptor.Input.Digest || contract.SHA256(fixture.Output) != descriptor.Output.Digest {
			return fmt.Errorf("fixture %q が manifest と一致しない", descriptor.Name)
		}
		if err := ValidateJSON(pkg.InputSchema, fixture.Input); err != nil {
			return fmt.Errorf("fixture %q input: %w", descriptor.Name, err)
		}
		if err := ValidateJSON(pkg.OutputSchema, fixture.Output); err != nil {
			return fmt.Errorf("fixture %q output: %w", descriptor.Name, err)
		}
	}
	return nil
}

func validateManifest(manifest Manifest) error {
	if manifest.Schema != contract.AgentPackageSchemaV1Alpha1 {
		return fmt.Errorf("Agent Package schema が不正: %q", manifest.Schema)
	}
	if !validIdentifier(manifest.Name) || manifest.Name != manifest.Operation {
		return fmt.Errorf("Agent Package name/operation が不正: %q / %q", manifest.Name, manifest.Operation)
	}
	if !strings.HasPrefix(manifest.Version, "v") || !validIdentifier(strings.TrimPrefix(manifest.Version, "v")) {
		return fmt.Errorf("Agent Package version が不正: %q", manifest.Version)
	}
	descriptors := []struct {
		name      string
		value     FileDescriptor
		mediaType string
	}{
		{"instructions", manifest.Instructions, contract.MediaTypeMarkdown},
		{"inputSchema", manifest.InputSchema, contract.MediaTypeJSON},
		{"outputSchema", manifest.OutputSchema, contract.MediaTypeJSON},
		{"toolProfile", manifest.ToolProfile, contract.MediaTypeJSON},
	}
	seenPaths := make(map[string]bool)
	for _, fixture := range manifest.Fixtures {
		if !validIdentifier(fixture.Name) {
			return fmt.Errorf("fixture name が不正: %q", fixture.Name)
		}
		descriptors = append(descriptors,
			struct {
				name      string
				value     FileDescriptor
				mediaType string
			}{"fixture input", fixture.Input, contract.MediaTypeJSON},
			struct {
				name      string
				value     FileDescriptor
				mediaType string
			}{"fixture output", fixture.Output, contract.MediaTypeJSON},
		)
	}
	if len(manifest.Fixtures) == 0 {
		return errors.New("Agent Package fixtures が空")
	}
	seenFixtures := make(map[string]bool)
	for _, fixture := range manifest.Fixtures {
		if seenFixtures[fixture.Name] {
			return fmt.Errorf("fixture name が重複: %s", fixture.Name)
		}
		seenFixtures[fixture.Name] = true
	}
	for _, descriptor := range descriptors {
		if !validRelativePath(descriptor.value.Path) {
			return fmt.Errorf("%s path が不正: %q", descriptor.name, descriptor.value.Path)
		}
		if seenPaths[descriptor.value.Path] {
			return fmt.Errorf("component path が重複: %s", descriptor.value.Path)
		}
		seenPaths[descriptor.value.Path] = true
		if descriptor.value.MediaType != descriptor.mediaType {
			return fmt.Errorf("%s mediaType が不正: %q", descriptor.name, descriptor.value.MediaType)
		}
		if !descriptor.value.Digest.Valid() {
			return fmt.Errorf("%s digest が不正: %q", descriptor.name, descriptor.value.Digest)
		}
	}
	return nil
}

func validateToolProfile(operation string, profile ToolProfile) error {
	if profile.Schema != ToolProfileSchemaV1Alpha1 {
		return fmt.Errorf("tool profile schema が不正: %q", profile.Schema)
	}
	if profile.Network != NetworkNone {
		return fmt.Errorf("tool profile network が不正: %q", profile.Network)
	}
	seen := make(map[string]bool)
	for _, capability := range profile.Capabilities {
		if !validCapability(capability) || seen[capability] {
			return fmt.Errorf("tool capability が不正または重複: %q", capability)
		}
		seen[capability] = true
		if operation == "test_validity" && capability != "repository:read" {
			return fmt.Errorf("test_validity v1alpha1 が宣言できる capability は repository:read だけ: %q", capability)
		}
	}
	return nil
}

func validateSchemaDocument(data []byte, idPrefix string) error {
	var schema map[string]any
	if err := decodeStrictJSON(data, &schema); err != nil {
		return err
	}
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		return fmt.Errorf("JSON Schema dialect が不正: %v", schema["$schema"])
	}
	id, ok := schema["$id"].(string)
	if !ok || !strings.HasPrefix(id, idPrefix) {
		return fmt.Errorf("JSON Schema $id が不正: %v", schema["$id"])
	}
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		return errors.New("JSON Schema root は additionalProperties:false の object でなければならない")
	}
	if err := validateSupportedSchema(schema, "$"); err != nil {
		return err
	}
	return validateSchemaReferences(schema, schema, "$")
}

var supportedSchemaKeywords = map[string]bool{
	"$schema": true, "$id": true, "$defs": true, "$ref": true,
	"type": true, "additionalProperties": true, "required": true, "properties": true,
	"items": true, "const": true, "enum": true, "pattern": true,
	"minItems": true, "maxItems": true, "minLength": true, "maxLength": true, "oneOf": true,
}

func validateSupportedSchema(schema map[string]any, location string) error {
	for keyword := range schema {
		if !supportedSchemaKeywords[keyword] {
			return fmt.Errorf("%s: 未対応 JSON Schema keyword %q", location, keyword)
		}
	}
	if value, exists := schema["$ref"]; exists {
		ref, ok := value.(string)
		if !ok || !strings.HasPrefix(ref, "#/$defs/") || strings.TrimPrefix(ref, "#/$defs/") == "" {
			return fmt.Errorf("%s.$ref がlocal $defs referenceでない", location)
		}
		// runtime validator は参照先を一つの schema node として評価する。sibling を
		// 許すと Draft 2020-12 の合成制約を黙って落とすため、本 subset では拒否する。
		if len(schema) != 1 {
			return fmt.Errorf("%s: $ref node は sibling keyword を持てない", location)
		}
	}
	if value, exists := schema["type"]; exists {
		if err := validateSchemaTypes(value, location+".type"); err != nil {
			return err
		}
		if schemaTypeContains(value, "object") && schema["additionalProperties"] != false {
			return fmt.Errorf("%s: object schemaはadditionalProperties:falseでなければならない", location)
		}
	}
	if value, exists := schema["additionalProperties"]; exists {
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s.additionalPropertiesがboolでない", location)
		}
	}
	if value, exists := schema["required"]; exists {
		list, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s.requiredがarrayでない", location)
		}
		seen := make(map[string]bool, len(list))
		for i, item := range list {
			name, ok := item.(string)
			if !ok || name == "" || seen[name] {
				return fmt.Errorf("%s.required[%d]が空、文字列でない、または重複", location, i)
			}
			seen[name] = true
		}
	}
	if value, exists := schema["enum"]; exists {
		list, ok := value.([]any)
		if !ok || len(list) == 0 {
			return fmt.Errorf("%s.enumが空またはarrayでない", location)
		}
	}
	if value, exists := schema["pattern"]; exists {
		pattern, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s.patternが文字列でない", location)
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("%s.patternが不正: %w", location, err)
		}
	}
	for _, keyword := range []string{"minItems", "maxItems", "minLength", "maxLength"} {
		if value, exists := schema[keyword]; exists {
			number, ok := value.(json.Number)
			if !ok {
				return fmt.Errorf("%s.%sがintegerでない", location, keyword)
			}
			integer, err := number.Int64()
			if err != nil || integer < 0 {
				return fmt.Errorf("%s.%sが非負integerでない", location, keyword)
			}
		}
	}
	for _, tableName := range []string{"properties", "$defs"} {
		tableValue, exists := schema[tableName]
		if !exists {
			continue
		}
		table, ok := tableValue.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.%s が object でない", location, tableName)
		}
		for name, childValue := range table {
			child, ok := childValue.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.%s.%s が object でない", location, tableName, name)
			}
			if err := validateSupportedSchema(child, location+"."+tableName+"."+name); err != nil {
				return err
			}
		}
	}
	if itemsValue, exists := schema["items"]; exists {
		items, ok := itemsValue.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.items が object でない", location)
		}
		if err := validateSupportedSchema(items, location+".items"); err != nil {
			return err
		}
	}
	if oneOfValue, exists := schema["oneOf"]; exists {
		oneOf, ok := oneOfValue.([]any)
		if !ok || len(oneOf) == 0 {
			return fmt.Errorf("%s.oneOf が空または array でない", location)
		}
		for i, childValue := range oneOf {
			child, ok := childValue.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.oneOf[%d] が object でない", location, i)
			}
			if err := validateSupportedSchema(child, fmt.Sprintf("%s.oneOf[%d]", location, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSchemaReferences(root, schema map[string]any, location string) error {
	if value, exists := schema["$ref"]; exists {
		name := strings.TrimPrefix(value.(string), "#/$defs/")
		if _, err := resolveDefinition(root, name); err != nil {
			return fmt.Errorf("%s.$ref: %w", location, err)
		}
	}
	for _, tableName := range []string{"properties", "$defs"} {
		table, _ := schema[tableName].(map[string]any)
		for name, value := range table {
			if err := validateSchemaReferences(root, value.(map[string]any), location+"."+tableName+"."+name); err != nil {
				return err
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		if err := validateSchemaReferences(root, items, location+".items"); err != nil {
			return err
		}
	}
	if oneOf, ok := schema["oneOf"].([]any); ok {
		for i, value := range oneOf {
			if err := validateSchemaReferences(root, value.(map[string]any), fmt.Sprintf("%s.oneOf[%d]", location, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

var supportedJSONTypes = map[string]bool{
	"object": true, "array": true, "string": true, "boolean": true,
	"number": true, "integer": true, "null": true,
}

func validateSchemaTypes(value any, location string) error {
	if name, ok := value.(string); ok {
		if supportedJSONTypes[name] {
			return nil
		}
		return fmt.Errorf("%sが未対応type: %q", location, name)
	}
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return fmt.Errorf("%sがtype名または非空arrayでない", location)
	}
	seen := make(map[string]bool, len(list))
	for i, item := range list {
		name, ok := item.(string)
		if !ok || !supportedJSONTypes[name] || seen[name] {
			return fmt.Errorf("%s[%d]が未知または重複type", location, i)
		}
		seen[name] = true
	}
	return nil
}

func schemaTypeContains(value any, want string) bool {
	if name, ok := value.(string); ok {
		return name == want
	}
	list, _ := value.([]any)
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func canonicalManifest(manifest Manifest) ([]byte, error) {
	canonical := cloneManifest(manifest)
	slices.SortFunc(canonical.Fixtures, func(a, b FixtureDescriptor) int { return strings.Compare(a.Name, b.Name) })
	return json.Marshal(canonical)
}

func decodeStrictJSON(data []byte, target any) error {
	if err := rejectDuplicateJSONNames(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("JSON document の後ろに余分な値がある")
	}
	return nil
}

func rejectDuplicateJSONNames(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("JSON document の後ろに余分な値がある")
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key が文字列でない")
			}
			if seen[key] {
				return fmt.Errorf("JSON object key が重複: %q", key)
			}
			seen[key] = true
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("JSON delimiter が不正: %q", delim)
	}
}

func readBounded(fsys fs.FS, name string) ([]byte, error) {
	file, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxComponentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxComponentBytes {
		return nil, fmt.Errorf("component が %d byte 上限を超えている", maxComponentBytes)
	}
	return data, nil
}

func validRelativePath(value string) bool {
	return value != "" && fs.ValidPath(value) && path.Clean(value) == value && value != "."
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i := range len(value) {
		c := value[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || (c == '_' || c == '-') && i > 0 {
			continue
		}
		return false
	}
	return true
}

func validCapability(value string) bool {
	parts := strings.Split(value, ":")
	return len(parts) == 2 && validIdentifier(parts[0]) && validIdentifier(parts[1])
}

func cloneManifest(manifest Manifest) Manifest {
	clone := manifest
	clone.Fixtures = append([]FixtureDescriptor(nil), manifest.Fixtures...)
	return clone
}

func cloneToolProfile(profile ToolProfile) ToolProfile {
	clone := profile
	clone.Capabilities = append([]string(nil), profile.Capabilities...)
	return clone
}

func cloneFixtures(fixtures []Fixture) []Fixture {
	clone := make([]Fixture, len(fixtures))
	for i, fixture := range fixtures {
		clone[i] = Fixture{Name: fixture.Name, Input: append([]byte(nil), fixture.Input...), Output: append([]byte(nil), fixture.Output...)}
	}
	return clone
}
