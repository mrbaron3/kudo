package github

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordSurfaceGoldenRoundTrip(t *testing.T) {
	t.Parallel()

	marker := validMarker()
	block := validMachineBlock()

	encodedMarker, err := EncodeMarker(marker)
	if err != nil {
		t.Fatalf("EncodeMarker() error = %v", err)
	}
	assertGolden(t, "marker.golden", encodedMarker)

	parsedMarker, err := ParseMarker(encodedMarker)
	if err != nil {
		t.Fatalf("ParseMarker() error = %v", err)
	}
	if parsedMarker != marker {
		t.Fatalf("ParseMarker() = %#v, want %#v", parsedMarker, marker)
	}

	encodedBlock, err := EncodeMachineBlock(block)
	if err != nil {
		t.Fatalf("EncodeMachineBlock() error = %v", err)
	}
	assertGolden(t, "machine-block.golden", encodedBlock)

	parsedBlock, err := ParseMachineBlock(encodedBlock)
	if err != nil {
		t.Fatalf("ParseMachineBlock() error = %v", err)
	}
	if parsedBlock.Kind != block.Kind || parsedBlock.MediaType != block.MediaType ||
		parsedBlock.Digest != block.Digest || string(parsedBlock.Payload) != string(block.Payload) {
		t.Fatalf("ParseMachineBlock() = %#v, want %#v", parsedBlock, block)
	}

	comment, err := RenderComment("レビュー結果です。", marker, &block)
	if err != nil {
		t.Fatalf("RenderComment() error = %v", err)
	}
	assertGolden(t, "comment.golden", comment)

	checkRunText, err := RenderCheckRunText(marker, &block)
	if err != nil {
		t.Fatalf("RenderCheckRunText() error = %v", err)
	}
	assertGolden(t, "check-run-output.golden", checkRunText)

	parsedSurface, err := ParseRecordSurface(comment)
	if err != nil {
		t.Fatalf("ParseRecordSurface() error = %v", err)
	}
	if parsedSurface.Marker != marker || parsedSurface.MachineBlock == nil ||
		string(parsedSurface.MachineBlock.Payload) != string(block.Payload) {
		t.Fatalf("ParseRecordSurface() = %#v", parsedSurface)
	}
}

func TestParseMarkerAcceptsUnknownFieldsButRejectsAmbiguity(t *testing.T) {
	t.Parallel()

	encoded, err := EncodeMarker(validMarker())
	if err != nil {
		t.Fatal(err)
	}
	forwardCompatible := strings.Replace(encoded, `}`, `,"future":"value"}`, 1)
	if _, err := ParseMarker(forwardCompatible); err != nil {
		t.Fatalf("future field must be ignored: %v", err)
	}

	ambiguous := strings.Replace(encoded, `"kind":"review-finding"`, `"kind":"review-finding","kind":"other"`, 1)
	if _, err := ParseMarker(ambiguous); !errors.Is(err, ErrInvalidRecordSurface) {
		t.Fatalf("duplicate field error = %v, want ErrInvalidRecordSurface", err)
	}
}

func TestRenderCommentRejectsRecordSurfaceOverflow(t *testing.T) {
	t.Parallel()

	_, err := RenderComment(strings.Repeat("x", MaxRecordSurfaceBytes), validMarker(), nil)
	if !errors.Is(err, ErrRecordSurfaceTooLarge) {
		t.Fatalf("RenderComment() error = %v, want ErrRecordSurfaceTooLarge", err)
	}
}

func validMarker() Marker {
	return Marker{
		Repository: Repository{Owner: "acme", Name: "widgets"},
		Issue:      16,
		Run:        "42",
		Kind:       "review-finding",
		Round:      2,
		Head:       strings.Repeat("a", 40),
		Digest:     "sha256:" + strings.Repeat("b", 64),
	}
}

func validMachineBlock() MachineBlock {
	return MachineBlock{
		Kind:      "review-result",
		MediaType: "application/yaml",
		Digest:    "sha256:" + strings.Repeat("b", 64),
		Payload:   []byte("schema: kudo.review-result/v1alpha1\nverdict: approve\n"),
	}
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", "record-surface", name))
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.TrimSuffix(string(want), "\n") {
		t.Fatalf("%s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, string(want))
	}
}
