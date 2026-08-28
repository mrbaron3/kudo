package agentpackage

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"testing/fstest"

	"github.com/mrbaron3/kudo/internal/contract"
)

func TestRepositoryTestValidityPackageIsSelfConsistent(t *testing.T) {
	t.Parallel()

	pkg, err := Load(os.DirFS("../.."), "agent-packages/test_validity/v1alpha1")
	if err != nil {
		t.Fatalf("Load(repository package) error = %v", err)
	}
	if pkg.Manifest.Name != "test_validity" || len(pkg.Fixtures) != 2 {
		t.Fatalf("package = %#v", pkg)
	}
}

func TestLoadValidatesPortablePackageClosure(t *testing.T) {
	t.Parallel()

	files := testPackageFiles(t)
	pkg, err := Load(files, ".")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if pkg.Ref.Schema != contract.AgentPackageSchemaV1Alpha1 || !pkg.Ref.Digest.Valid() {
		t.Fatalf("package ref = %#v", pkg.Ref)
	}
	if pkg.Manifest.Name != "test_validity" || pkg.Manifest.Operation != "test_validity" {
		t.Fatalf("manifest = %#v", pkg.Manifest)
	}
	if string(pkg.Instructions) != "test validity instructions\n" {
		t.Fatalf("instructions = %q", pkg.Instructions)
	}
	if len(pkg.Fixtures) != 1 || pkg.Fixtures[0].Name != "approve" {
		t.Fatalf("fixtures = %#v", pkg.Fixtures)
	}

	// caller が受け取った bytes を変更しても、load 済み package closure は変わらない。
	pkg.Instructions[0] = 'X'
	if err := Validate(pkg); err == nil {
		t.Fatal("load 後に改変された package closure を受理した")
	}
	again, err := Load(files, ".")
	if err != nil {
		t.Fatal(err)
	}
	if string(again.Instructions) != "test validity instructions\n" || again.Ref != pkg.Ref {
		t.Fatal("package が caller と mutable bytes を共有した")
	}
}

func TestLoadRejectsTamperedAndProviderSpecificPackage(t *testing.T) {
	t.Parallel()

	tests := map[string]func(fstest.MapFS){
		"component digest mismatch": func(files fstest.MapFS) {
			files["instructions.md"] = &fstest.MapFile{Data: []byte("tampered\n")}
		},
		"path traversal": func(files fstest.MapFS) {
			manifest := decodeTestManifest(t, files)
			manifest.Instructions.Path = "../instructions.md"
			files[ManifestFile] = jsonFile(t, manifest)
		},
		"provider field": func(files fstest.MapFS) {
			var raw map[string]any
			if err := json.Unmarshal(files[ManifestFile].Data, &raw); err != nil {
				t.Fatal(err)
			}
			raw["provider"] = "codex"
			files[ManifestFile] = jsonFile(t, raw)
		},
		"write capability": func(files fstest.MapFS) {
			profile := ToolProfile{
				Schema:       ToolProfileSchemaV1Alpha1,
				Capabilities: []string{"repository:write"},
				Network:      NetworkNone,
			}
			data, err := json.Marshal(profile)
			if err != nil {
				t.Fatal(err)
			}
			files["tool-profile.json"] = &fstest.MapFile{Data: data}
			manifest := decodeTestManifest(t, files)
			manifest.ToolProfile.Digest = contract.SHA256(data)
			files[ManifestFile] = jsonFile(t, manifest)
		},
		"undeclared read capability": func(files fstest.MapFS) {
			profile := ToolProfile{
				Schema:       ToolProfileSchemaV1Alpha1,
				Capabilities: []string{"repository:search"},
				Network:      NetworkNone,
			}
			data, err := json.Marshal(profile)
			if err != nil {
				t.Fatal(err)
			}
			files["tool-profile.json"] = &fstest.MapFile{Data: data}
			manifest := decodeTestManifest(t, files)
			manifest.ToolProfile.Digest = contract.SHA256(data)
			files[ManifestFile] = jsonFile(t, manifest)
		},
		"unsupported schema keyword": func(files fstest.MapFS) {
			var schema map[string]any
			if err := json.Unmarshal(files["input.schema.json"].Data, &schema); err != nil {
				t.Fatal(err)
			}
			schema["additionalProperty"] = false
			data, err := json.Marshal(schema)
			if err != nil {
				t.Fatal(err)
			}
			files["input.schema.json"] = &fstest.MapFile{Data: data}
			manifest := decodeTestManifest(t, files)
			manifest.InputSchema.Digest = contract.SHA256(data)
			files[ManifestFile] = jsonFile(t, manifest)
		},
		"ref with ignored sibling": func(files fstest.MapFS) {
			var schema map[string]any
			if err := json.Unmarshal(files["input.schema.json"].Data, &schema); err != nil {
				t.Fatal(err)
			}
			schema["$defs"] = map[string]any{"schema": map[string]any{"type": "string"}}
			properties := schema["properties"].(map[string]any)
			properties["schema"] = map[string]any{
				"$ref":  "#/$defs/schema",
				"const": "この制約を無視してはならない",
			}
			data, err := json.Marshal(schema)
			if err != nil {
				t.Fatal(err)
			}
			files["input.schema.json"] = &fstest.MapFile{Data: data}
			manifest := decodeTestManifest(t, files)
			manifest.InputSchema.Digest = contract.SHA256(data)
			files[ManifestFile] = jsonFile(t, manifest)
		},
		"unresolved local ref": func(files fstest.MapFS) {
			var schema map[string]any
			if err := json.Unmarshal(files["input.schema.json"].Data, &schema); err != nil {
				t.Fatal(err)
			}
			properties := schema["properties"].(map[string]any)
			properties["optional"] = map[string]any{"$ref": "#/$defs/missing"}
			data, err := json.Marshal(schema)
			if err != nil {
				t.Fatal(err)
			}
			files["input.schema.json"] = &fstest.MapFile{Data: data}
			manifest := decodeTestManifest(t, files)
			manifest.InputSchema.Digest = contract.SHA256(data)
			files[ManifestFile] = jsonFile(t, manifest)
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			files := cloneMapFS(testPackageFiles(t))
			mutate(files)
			if _, err := Load(files, "."); err == nil {
				t.Fatal("Load() error = nil")
			}
		})
	}
}

func testPackageFiles(t *testing.T) fstest.MapFS {
	t.Helper()
	contents := map[string][]byte{
		"instructions.md":              []byte("test validity instructions\n"),
		"input.schema.json":            []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"kudo.agent-input/test_validity/v1alpha1","type":"object","additionalProperties":false,"required":["schema"],"properties":{"schema":{"const":"kudo.agent-input/test_validity/v1alpha1"}}}`),
		"output.schema.json":           []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"kudo.agent-output/test_validity/v1alpha1","type":"object","additionalProperties":false,"required":["schema"],"properties":{"schema":{"const":"kudo.agent-output/test_validity/v1alpha1"}}}`),
		"tool-profile.json":            []byte(`{"schema":"kudo.agent-tool-profile/v1alpha1","capabilities":["repository:read"],"network":"none"}`),
		"fixtures/approve.input.json":  []byte(`{"schema":"kudo.agent-input/test_validity/v1alpha1"}`),
		"fixtures/approve.output.json": []byte(`{"schema":"kudo.agent-output/test_validity/v1alpha1"}`),
	}
	descriptor := func(name, mediaType string) FileDescriptor {
		return FileDescriptor{Path: name, MediaType: mediaType, Digest: contract.SHA256(contents[name])}
	}
	manifest := Manifest{
		Schema:       contract.AgentPackageSchemaV1Alpha1,
		Name:         "test_validity",
		Version:      "v1alpha1",
		Operation:    "test_validity",
		Instructions: descriptor("instructions.md", contract.MediaTypeMarkdown),
		InputSchema:  descriptor("input.schema.json", contract.MediaTypeJSON),
		OutputSchema: descriptor("output.schema.json", contract.MediaTypeJSON),
		ToolProfile:  descriptor("tool-profile.json", contract.MediaTypeJSON),
		Fixtures: []FixtureDescriptor{{
			Name:   "approve",
			Input:  descriptor("fixtures/approve.input.json", contract.MediaTypeJSON),
			Output: descriptor("fixtures/approve.output.json", contract.MediaTypeJSON),
		}},
	}
	files := make(fstest.MapFS, len(contents)+1)
	for name, data := range contents {
		files[name] = &fstest.MapFile{Data: append([]byte(nil), data...)}
	}
	files[ManifestFile] = jsonFile(t, manifest)
	return files
}

func decodeTestManifest(t *testing.T, files fstest.MapFS) Manifest {
	t.Helper()
	var manifest Manifest
	if err := json.Unmarshal(files[ManifestFile].Data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func jsonFile(t *testing.T, value any) *fstest.MapFile {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return &fstest.MapFile{Data: data}
}

func cloneMapFS(source fstest.MapFS) fstest.MapFS {
	clone := make(fstest.MapFS, len(source))
	for name, file := range source {
		if file == nil {
			panic(fmt.Sprintf("nil MapFile: %s", name))
		}
		copy := *file
		copy.Data = append([]byte(nil), file.Data...)
		clone[name] = &copy
	}
	return clone
}
