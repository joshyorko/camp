package campkit

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestManifestCanonicalSerializationIsDeterministic(t *testing.T) {
	first := validManifest()
	second := validManifest()
	second.SupportedArchitectures = []string{"arm64", "amd64"}
	second.Tools[0], second.Tools[1] = second.Tools[1], second.Tools[0]
	second.WorkspaceImages[0], second.WorkspaceImages[1] = second.WorkspaceImages[1], second.WorkspaceImages[0]

	firstBody, err := MarshalCanonical(first)
	if err != nil {
		t.Fatal(err)
	}
	secondBody, err := MarshalCanonical(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBody, secondBody) {
		t.Fatalf("semantically equal manifests differ:\nfirst:  %s\nsecond: %s", firstBody, secondBody)
	}
	if bytes.HasSuffix(firstBody, []byte("\n")) {
		t.Fatalf("canonical manifest has a trailing newline: %q", firstBody)
	}

	decoded, err := DecodeCanonical(firstBody)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := MarshalCanonical(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBody, roundTrip) {
		t.Fatalf("canonical round trip changed bytes:\nfirst: %s\nagain: %s", firstBody, roundTrip)
	}
}

func TestDecodeCanonicalRejectsUnknownAndNonCanonicalDocuments(t *testing.T) {
	body, err := MarshalCanonical(validManifest())
	if err != nil {
		t.Fatal(err)
	}

	unknown := append([]byte{}, body[:len(body)-1]...)
	unknown = append(unknown, []byte(`,"futureField":true}`)...)
	if _, err := DecodeCanonical(unknown); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}

	nonCanonical := append([]byte(" "), body...)
	if _, err := DecodeCanonical(nonCanonical); err == nil || !strings.Contains(err.Error(), "not canonical") {
		t.Fatalf("non-canonical document error = %v", err)
	}
}

func TestValidateRejectsIncompleteManifest(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"schema version", func(manifest *Manifest) { manifest.SchemaVersion = 0 }},
		{"Camp identity", func(manifest *Manifest) { manifest.Camp.SHA256 = "" }},
		{"capsule identity", func(manifest *Manifest) { manifest.Capsule.ID = "" }},
		{"capsule generation", func(manifest *Manifest) { manifest.Capsule.Generation = 0 }},
		{"runtime closure", func(manifest *Manifest) { manifest.Runtime = ArtifactIdentity{} }},
		{"tool closure", func(manifest *Manifest) { manifest.Tools = nil }},
		{"workspace images", func(manifest *Manifest) { manifest.WorkspaceImages = nil }},
		{"Room image", func(manifest *Manifest) { manifest.RoomImage = ImageIdentity{} }},
		{"DevPod provider", func(manifest *Manifest) { manifest.DevPodProvider = ArtifactIdentity{} }},
		{"architectures", func(manifest *Manifest) { manifest.SupportedArchitectures = nil }},
		{"trust", func(manifest *Manifest) { manifest.Trust.Status = "" }},
		{"lineage", func(manifest *Manifest) { manifest.Lineage.KitID = "" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManifest()
			test.mutate(&manifest)
			if err := Validate(manifest); err == nil {
				t.Fatal("incomplete manifest was accepted")
			}
		})
	}
}

func TestValidateRejectsUnsafeManifestIdentities(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"unsupported version", func(manifest *Manifest) { manifest.SchemaVersion = 2 }},
		{"capsule traversal", func(manifest *Manifest) { manifest.Capsule.ID = "../brain" }},
		{"mutable workspace image", func(manifest *Manifest) { manifest.WorkspaceImages[0].Digest = "" }},
		{"mutable Room image", func(manifest *Manifest) { manifest.RoomImage.Reference = "ghcr.io/joshyorko/room:latest" }},
		{"invalid artifact digest", func(manifest *Manifest) { manifest.Tools[0].SHA256 = "sha256:not-a-digest" }},
		{"unsupported architecture", func(manifest *Manifest) { manifest.SupportedArchitectures = []string{"amd64", "s390x"} }},
		{"duplicate architecture", func(manifest *Manifest) { manifest.SupportedArchitectures = []string{"amd64", "amd64"} }},
		{"duplicate tool", func(manifest *Manifest) { manifest.Tools[1] = manifest.Tools[0] }},
		{"duplicate image", func(manifest *Manifest) { manifest.WorkspaceImages[1] = manifest.WorkspaceImages[0] }},
		{"future export time", func(manifest *Manifest) { manifest.Lineage.ExportedAt = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManifest()
			test.mutate(&manifest)
			if err := Validate(manifest); err == nil {
				t.Fatal("unsafe manifest was accepted")
			}
		})
	}
}

func TestValidateEnforcesTrustStatusEvidence(t *testing.T) {
	verified := validManifest()
	verified.Trust = TrustMetadata{
		Status:     TrustVerified,
		VerifiedBy: "sha256:" + strings.Repeat("9", 64),
		VerifiedAt: verified.Lineage.ExportedAt,
		Signature: &ArtifactIdentity{
			Name:    "cosign-bundle",
			Version: "v1",
			SHA256:  strings.Repeat("8", 64),
		},
	}
	if err := Validate(verified); err != nil {
		t.Fatalf("verified trust evidence rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*TrustMetadata)
	}{
		{"verified without verifier", func(trust *TrustMetadata) { trust.VerifiedBy = "" }},
		{"verified without verification time", func(trust *TrustMetadata) { trust.VerifiedAt = time.Time{} }},
		{"verified without signature", func(trust *TrustMetadata) { trust.Signature = nil }},
		{"unverified with claimed verifier", func(trust *TrustMetadata) { trust.Status = TrustUnverified }},
		{"unknown status", func(trust *TrustMetadata) { trust.Status = TrustStatus("trusted") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := verified
			test.mutate(&manifest.Trust)
			if err := Validate(manifest); err == nil {
				t.Fatal("inconsistent trust metadata was accepted")
			}
		})
	}
}

func validManifest() Manifest {
	exportedAt := time.Date(2026, 7, 23, 15, 30, 0, 0, time.UTC)
	return Manifest{
		SchemaVersion: 1,
		Camp: ArtifactIdentity{
			Name: "camp", Version: "v0.1.0", SHA256: strings.Repeat("1", 64),
		},
		Capsule: CapsuleGeneration{
			ID: "second-brain", Generation: 42, ArchiveSHA256: strings.Repeat("2", 64),
		},
		Runtime: ArtifactIdentity{
			Name: "linux-runtime", Version: "v1", SHA256: strings.Repeat("3", 64),
		},
		Tools: []ArtifactIdentity{
			{Name: "hauler", Version: "v2.0.2", SHA256: strings.Repeat("5", 64)},
			{Name: "devpod", Version: "v0.26.1", SHA256: strings.Repeat("4", 64)},
		},
		WorkspaceImages: []ImageIdentity{
			{Reference: "ghcr.io/example/workspace-b@sha256:" + strings.Repeat("b", 64), Digest: "sha256:" + strings.Repeat("b", 64)},
			{Reference: "ghcr.io/example/workspace-a@sha256:" + strings.Repeat("a", 64), Digest: "sha256:" + strings.Repeat("a", 64)},
		},
		RoomImage: ImageIdentity{
			Reference: "ghcr.io/joshyorko/room@sha256:" + strings.Repeat("c", 64),
			Digest:    "sha256:" + strings.Repeat("c", 64),
		},
		DevPodProvider: ArtifactIdentity{
			Name: "docker-provider", Version: "v0.1.0", SHA256: strings.Repeat("6", 64),
		},
		SupportedArchitectures: []string{"amd64", "arm64"},
		Trust:                  TrustMetadata{Status: TrustUnverified},
		Lineage: KitLineage{
			KitID:               "kit-20260723T153000Z",
			ExportedAt:          exportedAt,
			SourceKitSHA256:     strings.Repeat("7", 64),
			ParentKitSHA256:     strings.Repeat("8", 64),
			SourceGenerationSHA: strings.Repeat("2", 64),
		},
	}
}
