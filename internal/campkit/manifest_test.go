package campkit

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	digest1 = "1111111111111111111111111111111111111111111111111111111111111111"
	digest2 = "2222222222222222222222222222222222222222222222222222222222222222"
	digest3 = "3333333333333333333333333333333333333333333333333333333333333333"
	digest4 = "4444444444444444444444444444444444444444444444444444444444444444"
	digest5 = "5555555555555555555555555555555555555555555555555555555555555555"
)

func TestMarshalCanonicalPermutationAndRoundTrip(t *testing.T) {
	first := validManifest()
	second := cloneManifest(first)
	reverse(second.SupportedPlatforms)
	reverse(second.Payloads)
	reverse(second.Images)

	firstBody, err := MarshalCanonical(first)
	if err != nil {
		t.Fatal(err)
	}
	secondBody, err := MarshalCanonical(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBody, secondBody) {
		t.Fatalf("permutations differ:\nfirst:  %s\nsecond: %s", firstBody, secondBody)
	}
	if bytes.HasSuffix(firstBody, []byte("\n")) {
		t.Fatal("canonical bytes have a trailing newline")
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
		t.Fatalf("round trip changed bytes:\nfirst: %s\nagain: %s", firstBody, roundTrip)
	}
}

func TestMarshalCanonicalDeepCopiesAndNormalizesTimes(t *testing.T) {
	m := validManifest()
	offset := time.FixedZone("offset", -5*60*60)
	exported := m.Lineage.ExportedAt.In(offset)
	verified := exported.Add(-time.Minute)
	m.Lineage.ExportedAt = exported
	m.Trust = verifiedTrust(&verified)
	addTrustEvidence(&m)
	before := cloneManifest(m)

	body, err := MarshalCanonical(m)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m, before) {
		t.Fatalf("marshal mutated input:\nbefore: %#v\nafter:  %#v", before, m)
	}
	if !bytes.Contains(body, []byte(`"exportedAt":"2026-07-23T15:30:00Z"`)) {
		t.Fatalf("exportedAt was not normalized to UTC: %s", body)
	}
	if bytes.Contains(body, []byte("-05:00")) {
		t.Fatalf("offset survived canonicalization: %s", body)
	}
}

func TestMarshalCanonicalOmitsZeroVerifiedAt(t *testing.T) {
	body, err := MarshalCanonical(validManifest())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("verifiedAt")) {
		t.Fatalf("zero verifiedAt was emitted: %s", body)
	}
}

func TestDecodeCanonicalStrictnessAndSchemaClassification(t *testing.T) {
	body := mustCanonical(t, validManifest())
	unknown := append(append([]byte{}, body[:len(body)-1]...), []byte(`,"credential":"secret"}`)...)
	v1 := bytes.Replace(body, []byte(`"schemaVersion":2`), []byte(`"schemaVersion":1`), 1)
	oversized := bytes.Repeat([]byte(" "), maxManifestBytes+1)
	tests := []struct {
		name        string
		body        []byte
		wantAs      bool
		wantContain string
	}{
		{"unsupported v1", v1, true, "unsupported schema version 1"},
		{"malformed JSON", []byte(`{"schemaVersion":`), false, "unexpected"},
		{"unknown field", unknown, false, "unknown field"},
		{"trailing value", append(append([]byte{}, body...), []byte(`{}`)...), false, "trailing"},
		{"leading whitespace", append([]byte(" "), body...), false, "not canonical"},
		{"UTC offset", bytes.Replace(body, []byte(`"2026-07-23T15:30:00Z"`), []byte(`"2026-07-23T11:30:00-04:00"`), 1), false, "not canonical"},
		{"oversized", oversized, false, "maximum"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeCanonical(test.body)
			if err == nil {
				t.Fatal("document was accepted")
			}
			var schemaErr *UnsupportedSchemaError
			if got := errors.As(err, &schemaErr); got != test.wantAs {
				t.Fatalf("errors.As(UnsupportedSchemaError) = %v, want %v (err: %v)", got, test.wantAs, err)
			}
			if test.wantContain != "" && !strings.Contains(err.Error(), test.wantContain) {
				t.Fatalf("error %q does not contain %q", err, test.wantContain)
			}
		})
	}
}

func TestValidateGenerationAndMetadataBindings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"zero generation", func(m *Manifest) { m.Generation.Ref.Generation = 0 }},
		{"zero parent", func(m *Manifest) { m.Generation.Parent.Generation = 0 }},
		{"parent not before generation", func(m *Manifest) { m.Generation.Parent.Generation = m.Generation.Ref.Generation }},
		{"lineage generation mismatch", func(m *Manifest) { m.Lineage.SourceGeneration.Generation++ }},
		{"lineage digest mismatch", func(m *Manifest) { m.Lineage.SourceGeneration.ArchiveSHA256 = digest2 }},
		{"archive missing", func(m *Manifest) { removePayload(m, PayloadGenerationArchive, "generation") }},
		{"archive path mismatch", func(m *Manifest) {
			payload(m, PayloadGenerationArchive, "generation").Path = "payloads/generation/other.tar"
		}},
		{"archive digest mismatch", func(m *Manifest) { payload(m, PayloadGenerationArchive, "generation").SHA256 = digest2 }},
		{"archive platform", func(m *Manifest) { p := amd64(); payload(m, PayloadGenerationArchive, "generation").Platform = &p }},
		{"duplicate archive", func(m *Manifest) {
			m.Payloads = append(m.Payloads, clonePayload(*payload(m, PayloadGenerationArchive, "generation")))
			m.Payloads[len(m.Payloads)-1].Path = "payloads/generation/duplicate.tar"
		}},
		{"metadata missing", func(m *Manifest) { removePayload(m, PayloadGenerationMetadata, "generation") }},
		{"metadata path mismatch", func(m *Manifest) {
			payload(m, PayloadGenerationMetadata, "generation").Path = "payloads/generation/other.json"
		}},
		{"metadata zero size", func(m *Manifest) { payload(m, PayloadGenerationMetadata, "generation").Size = 0 }},
		{"metadata digest missing", func(m *Manifest) { payload(m, PayloadGenerationMetadata, "generation").SHA256 = "" }},
		{"duplicate metadata", func(m *Manifest) {
			m.Payloads = append(m.Payloads, clonePayload(*payload(m, PayloadGenerationMetadata, "generation")))
			m.Payloads[len(m.Payloads)-1].Path = "payloads/generation/duplicate.json"
		}},
	}
	assertInvalidMutations(t, tests)
}

func TestValidatePlatformAndToolClosure(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"no platforms", func(m *Manifest) { m.SupportedPlatforms = nil }},
		{"too many platforms", func(m *Manifest) {
			m.SupportedPlatforms = append(m.SupportedPlatforms, Platform{OS: "linux", Architecture: "s390x"})
		}},
		{"unsupported OS", func(m *Manifest) { m.SupportedPlatforms[0].OS = "darwin" }},
		{"unsupported architecture", func(m *Manifest) { m.SupportedPlatforms[0].Architecture = "s390x" }},
		{"variant", func(m *Manifest) { m.SupportedPlatforms[0].Variant = "v8" }},
		{"duplicate platform", func(m *Manifest) { m.SupportedPlatforms[1] = m.SupportedPlatforms[0] }},
		{"Camp missing arm64", func(m *Manifest) { removePayloadForPlatform(m, PayloadCamp, "camp", arm64()) }},
		{"extra Camp arm64", func(m *Manifest) {
			p := clonePayload(*payloadForPlatform(m, PayloadCamp, "camp", arm64()))
			p.Name = "other-camp"
			p.Path = "payloads/arm64/other-camp"
			m.Payloads = append(m.Payloads, p)
		}},
		{"runtime missing amd64", func(m *Manifest) { removePayloadForPlatform(m, PayloadRuntime, "runtime", amd64()) }},
		{"provider missing arm64", func(m *Manifest) { removePayloadForPlatform(m, PayloadDevPodProvider, "docker", arm64()) }},
		{"DevPod missing amd64", func(m *Manifest) { removePayloadForPlatform(m, PayloadTool, "devpod", amd64()) }},
		{"Hauler missing arm64", func(m *Manifest) { removePayloadForPlatform(m, PayloadTool, "hauler", arm64()) }},
		{"executable role without platform", func(m *Manifest) { payloadForPlatform(m, PayloadCamp, "camp", amd64()).Platform = nil }},
		{"trust evidence with platform", func(m *Manifest) {
			when := m.Lineage.ExportedAt
			m.Trust = verifiedTrust(&when)
			addTrustEvidence(m)
			p := amd64()
			payload(m, PayloadTrustEvidence, "verification").Platform = &p
		}},
		{"tool repository missing", func(m *Manifest) { payloadForPlatform(m, PayloadTool, "devpod", amd64()).Repository = "" }},
		{"tool version missing", func(m *Manifest) { payloadForPlatform(m, PayloadTool, "devpod", amd64()).Version = "" }},
		{"tool commit malformed", func(m *Manifest) { payloadForPlatform(m, PayloadTool, "devpod", amd64()).Commit = "ABC" }},
		{"tool executable digest missing", func(m *Manifest) { payloadForPlatform(m, PayloadTool, "hauler", amd64()).ExecutableSHA256 = "" }},
		{"DevPod executable differs from asset", func(m *Manifest) { payloadForPlatform(m, PayloadTool, "devpod", amd64()).ExecutableSHA256 = digest2 }},
	}
	assertInvalidMutations(t, tests)
}

func TestValidateAcceptsLockedToolIdentities(t *testing.T) {
	if err := Validate(validManifest()); err != nil {
		t.Fatalf("fixture representing tools.lock.yaml was rejected: %v", err)
	}
}

func TestValidatePayloadPaths(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"empty", ""},
		{"absolute", "/payloads/camp"},
		{"traversal", "payloads/../camp"},
		{"dot component", "payloads/./camp"},
		{"empty component", "payloads//camp"},
		{"backslash", `payloads\camp`},
		{"drive", "payloads/C:/camp"},
		{"URL scheme", "payloads/https://host/file"},
		{"query", "payloads/camp?token=x"},
		{"fragment", "payloads/camp#fragment"},
		{"NUL", "payloads/camp\x00"},
		{"non-ASCII", "payloads/café"},
		{"outside prefix", "other/camp"},
		{"manifest", "manifest.json"},
		{"too long", "payloads/" + strings.Repeat("a", 247)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := validManifest()
			m.Payloads[0].Path = test.path
			if err := Validate(m); err == nil {
				t.Fatalf("path %q was accepted", test.path)
			}
		})
	}
	t.Run("duplicate", func(t *testing.T) {
		m := validManifest()
		m.Payloads[1].Path = m.Payloads[0].Path
		if err := Validate(m); err == nil {
			t.Fatal("duplicate path accepted")
		}
	})
	t.Run("ancestor collision", func(t *testing.T) {
		m := validManifest()
		m.Payloads[0].Path = "payloads/collision"
		m.Payloads[1].Path = "payloads/collision/child"
		if err := Validate(m); err == nil {
			t.Fatal("ancestor collision accepted")
		}
	})
}

func TestValidatePayloadSizesAndBounds(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"zero", func(m *Manifest) { m.Payloads[0].Size = 0 }},
		{"negative", func(m *Manifest) { m.Payloads[0].Size = -1 }},
		{"aggregate overflow", func(m *Manifest) { m.Payloads[0].Size = math.MaxInt64; m.Payloads[1].Size = 1 }},
		{"too many payloads", func(m *Manifest) {
			for len(m.Payloads) <= maxPayloads {
				p := clonePayload(m.Payloads[len(m.Payloads)%10])
				p.Name = fmt.Sprintf("extra-%03d", len(m.Payloads))
				p.Path = fmt.Sprintf("payloads/extra/%03d", len(m.Payloads))
				m.Payloads = append(m.Payloads, p)
			}
		}},
		{"too many images", func(m *Manifest) {
			for len(m.Images) <= maxImages {
				p := amd64()
				m.Images = append(m.Images, ImageIdentity{Role: ImageWorkspace, Reference: fmt.Sprintf("registry.example/workspace-%04d@sha256:%s", len(m.Images), digest1), Digest: "sha256:" + digest1, Platform: p})
			}
		}},
		{"oversized string", func(m *Manifest) { m.Payloads[0].MediaType = strings.Repeat("a", maxStringBytes+1) }},
	}
	assertInvalidMutations(t, tests)
}

func TestValidateOCIReferencesAndImageClosure(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"missing Room arm64", func(m *Manifest) { removeImageForPlatform(m, ImageRoom, arm64()) }},
		{"unsupported image platform", func(m *Manifest) { m.Images[0].Platform.Architecture = "s390x" }},
		{"mutable tag", func(m *Manifest) { m.Images[0].Reference = "ghcr.io/joshyorko/room:latest" }},
		{"tag plus digest", func(m *Manifest) { m.Images[0].Reference = "ghcr.io/joshyorko/room:v1@sha256:" + digest1 }},
		{"scheme", func(m *Manifest) { m.Images[0].Reference = "https://ghcr.io/joshyorko/room@sha256:" + digest1 }},
		{"credentials", func(m *Manifest) { m.Images[0].Reference = "user:pass@ghcr.io/room@sha256:" + digest1 }},
		{"query", func(m *Manifest) { m.Images[0].Reference += "?token=x" }},
		{"fragment", func(m *Manifest) { m.Images[0].Reference += "#x" }},
		{"whitespace", func(m *Manifest) { m.Images[0].Reference = "ghcr.io/josh yorko/room@sha256:" + digest1 }},
		{"bad digest", func(m *Manifest) { m.Images[0].Digest = "sha256:ABC" }},
		{"reference digest mismatch", func(m *Manifest) { m.Images[0].Digest = "sha256:" + digest2 }},
		{"index ambiguity", func(m *Manifest) {
			m.Images[1].Reference = m.Images[0].Reference
			m.Images[1].Digest = m.Images[0].Digest
		}},
		{"duplicate semantic identity", func(m *Manifest) { m.Images = append(m.Images, m.Images[0]) }},
	}
	assertInvalidMutations(t, tests)
}

func TestValidateAcceptsDigestPinnedShortOCIName(t *testing.T) {
	m := validManifest()
	m.Images[0].Reference = "room@sha256:" + digest1
	if err := Validate(m); err != nil {
		t.Fatalf("valid short OCI name was rejected: %v", err)
	}
}

func TestValidatePayloadSemanticIdentities(t *testing.T) {
	m := validManifest()
	duplicate := clonePayload(m.Payloads[0])
	duplicate.Path = "payloads/duplicate/camp"
	m.Payloads = append(m.Payloads, duplicate)
	if err := Validate(m); err == nil {
		t.Fatal("duplicate payload semantic identity accepted")
	}

	m = validManifest()
	camp := payloadForPlatform(&m, PayloadCamp, "camp", amd64())
	runtime := payloadForPlatform(&m, PayloadRuntime, "runtime", amd64())
	camp.SHA256 = runtime.SHA256
	camp.ExecutableSHA256 = runtime.ExecutableSHA256
	if err := Validate(m); err != nil {
		t.Fatalf("byte reuse under distinct identities was rejected: %v", err)
	}
}

func TestValidateTrustMatrices(t *testing.T) {
	exported := validManifest().Lineage.ExportedAt
	after := exported.Add(time.Second)
	zero := time.Time{}
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"unverified verifier", func(m *Manifest) { m.Trust.Verifier = "sha256:" + digest1 }},
		{"unverified time", func(m *Manifest) { m.Trust.VerifiedAt = &exported }},
		{"unverified path", func(m *Manifest) { m.Trust.EvidencePath = "payloads/trust/evidence.json" }},
		{"unverified payload", func(m *Manifest) { addTrustEvidence(m) }},
		{"verified verifier missing", func(m *Manifest) { m.Trust = verifiedTrust(&exported); m.Trust.Verifier = ""; addTrustEvidence(m) }},
		{"verified time missing", func(m *Manifest) { m.Trust = verifiedTrust(nil); addTrustEvidence(m) }},
		{"verified zero time", func(m *Manifest) { m.Trust = verifiedTrust(&zero); addTrustEvidence(m) }},
		{"verified after export", func(m *Manifest) { m.Trust = verifiedTrust(&after); addTrustEvidence(m) }},
		{"verified evidence missing", func(m *Manifest) { m.Trust = verifiedTrust(&exported) }},
		{"verified evidence path mismatch", func(m *Manifest) {
			m.Trust = verifiedTrust(&exported)
			addTrustEvidence(m)
			m.Trust.EvidencePath = "payloads/trust/other.json"
		}},
		{"verified duplicate evidence", func(m *Manifest) {
			m.Trust = verifiedTrust(&exported)
			addTrustEvidence(m)
			p := clonePayload(*payload(m, PayloadTrustEvidence, "verification"))
			p.Name = "other"
			p.Path = "payloads/trust/other.json"
			m.Payloads = append(m.Payloads, p)
		}},
		{"rejected incomplete", func(m *Manifest) { m.Trust.Status = TrustRejected }},
		{"unknown", func(m *Manifest) { m.Trust.Status = "trusted" }},
	}
	assertInvalidMutations(t, tests)

	for _, status := range []TrustStatus{TrustVerified, TrustRejected} {
		t.Run(string(status), func(t *testing.T) {
			m := validManifest()
			m.Trust = verifiedTrust(&exported)
			m.Trust.Status = status
			addTrustEvidence(&m)
			if err := Validate(m); err != nil {
				t.Fatalf("%s trust rejected: %v", status, err)
			}
		})
	}
}

func TestValidateHostileStringsAndFieldSurface(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"capsule control", func(m *Manifest) { m.Generation.Capsule = "cap\nsule" }},
		{"branch traversal", func(m *Manifest) { m.Generation.Branch = ".." }},
		{"payload name NUL", func(m *Manifest) { m.Payloads[0].Name = "camp\x00" }},
		{"repository control", func(m *Manifest) {
			payloadForPlatform(m, PayloadTool, "devpod", amd64()).Repository = "skevetter/\ndevpod"
		}},
		{"kit host path", func(m *Manifest) { m.Lineage.KitID = "/etc/passwd" }},
		{"verifier control", func(m *Manifest) {
			when := m.Lineage.ExportedAt
			m.Trust = verifiedTrust(&when)
			m.Trust.Verifier = "sha256:\n" + digest1
			addTrustEvidence(m)
		}},
		{"unknown role", func(m *Manifest) { m.Payloads[0].Role = "credential" }},
	}
	assertInvalidMutations(t, tests)

	body := mustCanonical(t, validManifest())
	for _, field := range []string{"hostPath", "environment", "providerConfig", "kubeconfig", "token", "port", "credentials"} {
		t.Run(field, func(t *testing.T) {
			hostile := append(append([]byte{}, body[:len(body)-1]...), []byte(fmt.Sprintf(`,%q:"secret"}`, field))...)
			if _, err := DecodeCanonical(hostile); err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("field %q error = %v", field, err)
			}
		})
	}
}

func assertInvalidMutations(t *testing.T, tests []struct {
	name   string
	mutate func(*Manifest)
}) {
	t.Helper()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := validManifest()
			test.mutate(&m)
			if err := Validate(m); err == nil {
				t.Fatal("invalid manifest was accepted")
			}
		})
	}
}

func validManifest() Manifest {
	exportedAt := time.Date(2026, 7, 23, 15, 30, 0, 0, time.UTC)
	parent := GenerationRef{Generation: 41, ArchiveSHA256: digest2}
	m := Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Generation: GenerationIdentity{
			Capsule: "second-brain", Branch: "main",
			Ref:    GenerationRef{Generation: 42, ArchiveSHA256: digest1},
			Parent: &parent, ArchivePath: "payloads/generation/archive.tar.zst",
			MetadataPath: "payloads/generation/metadata.json",
		},
		SupportedPlatforms: []Platform{amd64(), arm64()},
		Trust:              TrustMetadata{Status: TrustUnverified},
		Lineage: KitLineage{
			KitID: "kit-20260723T153000Z", ExportedAt: exportedAt,
			SourceGeneration: GenerationRef{Generation: 42, ArchiveSHA256: digest1},
			ParentKitSHA256:  digest5,
		},
	}
	m.Payloads = []PayloadIdentity{
		{Role: PayloadGenerationArchive, Name: "generation", Path: m.Generation.ArchivePath, Size: 100, SHA256: digest1, MediaType: "application/vnd.camp.generation-archive"},
		{Role: PayloadGenerationMetadata, Name: "generation", Path: m.Generation.MetadataPath, Size: 200, SHA256: digest2, MediaType: "application/json"},
	}
	for _, p := range m.SupportedPlatforms {
		key := p.Architecture
		m.Payloads = append(m.Payloads,
			executablePayload(PayloadCamp, "camp", "v0.1.0", "joshyorko/camp", strings.Repeat("a", 40), p, "payloads/"+key+"/camp", digest3, digest3),
			executablePayload(PayloadRuntime, "runtime", "v1", "joshyorko/runtime", strings.Repeat("b", 40), p, "payloads/"+key+"/runtime", digest4, digest4),
			executablePayload(PayloadDevPodProvider, "docker", "v0.1.0", "loft-sh/devpod-provider-docker", strings.Repeat("c", 40), p, "payloads/"+key+"/provider", digest5, digest5),
		)
		devpodDigest := "01bbc2d88090d546e04aa435c63fc5eb95ec49ffb7ab102a67de0d6d12c82d8d"
		haulerDigest := "d96ac67cac3c9e4fc2d24c8347fba956b2a165a2237318cc2564e44bbaabc4c3"
		if p.Architecture == "arm64" {
			devpodDigest = "268621da428ca6d470d1812d63c9e41a1681b681861fc984648a57c5725478ee"
			haulerDigest = "e77a7d2b707ba2ffbb5a69e1f6cacbf046065333cd9b1abe51ed8f9f099c2870"
		}
		m.Payloads = append(m.Payloads,
			executablePayload(PayloadTool, "devpod", "v0.26.1", "skevetter/devpod", "86b6f9f5d6713fecdeff5dd240e775a8c7e8d44e", p, "payloads/"+key+"/devpod", devpodDigest, devpodDigest),
			executablePayload(PayloadTool, "hauler", "v2.0.2", "hauler-dev/hauler", "4ece589a5c763fff15e253735263bd13a889d3cc", p, "payloads/"+key+"/hauler.tar.gz", haulerDigest, digest5),
		)
		roomDigest := digest1
		if p.Architecture == "arm64" {
			roomDigest = digest2
		}
		m.Images = append(m.Images, ImageIdentity{
			Role: ImageRoom, Reference: "ghcr.io/joshyorko/room-of-requirement@sha256:" + roomDigest,
			Digest: "sha256:" + roomDigest, Platform: p,
		})
	}
	return m
}

func executablePayload(role PayloadRole, name, version, repository, commit string, platform Platform, path, digest, executableDigest string) PayloadIdentity {
	p := platform
	return PayloadIdentity{
		Role: role, Name: name, Version: version, Repository: repository, Commit: commit,
		Platform: &p, Path: path, Size: 10, SHA256: digest, ExecutableSHA256: executableDigest,
		MediaType: "application/octet-stream",
	}
}

func verifiedTrust(when *time.Time) TrustMetadata {
	return TrustMetadata{
		Status: TrustVerified, Verifier: "sha256:" + digest3,
		VerifiedAt: when, EvidencePath: "payloads/trust/evidence.json",
	}
}

func addTrustEvidence(m *Manifest) {
	m.Payloads = append(m.Payloads, PayloadIdentity{
		Role: PayloadTrustEvidence, Name: "verification", Path: "payloads/trust/evidence.json",
		Size: 50, SHA256: digest4, MediaType: "application/vnd.camp.trust-evidence+json",
	})
}

func amd64() Platform { return Platform{OS: "linux", Architecture: "amd64"} }
func arm64() Platform { return Platform{OS: "linux", Architecture: "arm64"} }

func payload(m *Manifest, role PayloadRole, name string) *PayloadIdentity {
	for i := range m.Payloads {
		if m.Payloads[i].Role == role && m.Payloads[i].Name == name {
			return &m.Payloads[i]
		}
	}
	panic("payload not found")
}

func payloadForPlatform(m *Manifest, role PayloadRole, name string, platform Platform) *PayloadIdentity {
	for i := range m.Payloads {
		p := &m.Payloads[i]
		if p.Role == role && p.Name == name && p.Platform != nil && *p.Platform == platform {
			return p
		}
	}
	panic("payload not found")
}

func removePayload(m *Manifest, role PayloadRole, name string) {
	for i := range m.Payloads {
		if m.Payloads[i].Role == role && m.Payloads[i].Name == name {
			m.Payloads = append(m.Payloads[:i], m.Payloads[i+1:]...)
			return
		}
	}
}

func removePayloadForPlatform(m *Manifest, role PayloadRole, name string, platform Platform) {
	for i := range m.Payloads {
		p := m.Payloads[i]
		if p.Role == role && p.Name == name && p.Platform != nil && *p.Platform == platform {
			m.Payloads = append(m.Payloads[:i], m.Payloads[i+1:]...)
			return
		}
	}
}

func removeImageForPlatform(m *Manifest, role ImageRole, platform Platform) {
	for i := range m.Images {
		if m.Images[i].Role == role && m.Images[i].Platform == platform {
			m.Images = append(m.Images[:i], m.Images[i+1:]...)
			return
		}
	}
}

func reverse[T any](values []T) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func cloneManifest(m Manifest) Manifest {
	c := m
	c.SupportedPlatforms = append([]Platform(nil), m.SupportedPlatforms...)
	c.Payloads = make([]PayloadIdentity, len(m.Payloads))
	for i := range m.Payloads {
		c.Payloads[i] = clonePayload(m.Payloads[i])
	}
	c.Images = append([]ImageIdentity(nil), m.Images...)
	if m.Generation.Parent != nil {
		parent := *m.Generation.Parent
		c.Generation.Parent = &parent
	}
	if m.Trust.VerifiedAt != nil {
		verifiedAt := *m.Trust.VerifiedAt
		c.Trust.VerifiedAt = &verifiedAt
	}
	return c
}

func clonePayload(p PayloadIdentity) PayloadIdentity {
	c := p
	if p.Platform != nil {
		platform := *p.Platform
		c.Platform = &platform
	}
	return c
}

func mustCanonical(t *testing.T, m Manifest) []byte {
	t.Helper()
	body, err := MarshalCanonical(m)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
