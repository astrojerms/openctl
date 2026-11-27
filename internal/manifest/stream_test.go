package manifest

import (
	"testing"
)

const streamFirst = "apiVersion: example.openctl.io/v1\nkind: VirtualMachine\nmetadata:\n  name: first\n"
const streamSecond = "apiVersion: example.openctl.io/v1\nkind: VirtualMachine\nmetadata:\n  name: second\n"

func TestSingleManifestRejectsAdditionalResources(t *testing.T) {
	resource, err := Parse([]byte(streamFirst + "---\n" + streamSecond))
	if err == nil || resource != nil {
		t.Fatal("single-resource Apply would silently ignore a declared resource")
	}
}

func TestManifestStreamUsesYAMLDocumentBoundaries(t *testing.T) {
	for _, input := range []string{
		"# leading empty document\n---\n" + streamFirst + "---\n# empty middle\n---\n" + streamSecond + "---\n# empty tail\n",
		"%YAML 1.1\n---\n" + streamFirst + "---\n" + streamSecond,
		streamFirst + "----: a valid ordinary YAML key, not a document separator\n---\n" + streamSecond,
	} {
		resources, err := ParseMultiple([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if len(resources) != 2 || resources[0].Metadata.Name != "first" || resources[1].Metadata.Name != "second" {
			t.Fatalf("manifest stream changed resource identities or order: %v", resources)
		}
	}
}

func TestSingleManifestAllowsOnlyEmptyTrailingDocuments(t *testing.T) {
	resource, err := Parse([]byte(streamFirst + "---\n# trailing comments\n"))
	if err != nil || resource == nil || resource.Metadata.Name != "first" {
		t.Fatalf("empty trailing document rejected a single declaration: %v", err)
	}
}

func TestMalformedTrailingManifestNeverReturnsPartialIntent(t *testing.T) {
	for _, suffix := range []string{"---\n[unterminated", "---\nnull\n"} {
		input := []byte(streamFirst + suffix)
		single, err := Parse(input)
		if err == nil || single != nil {
			t.Fatal("single-resource parser ignored malformed trailing intent")
		}
		batch, err := ParseMultiple(input)
		if err == nil || batch != nil {
			t.Fatal("stream parser returned partial or malformed intent")
		}
	}
}
