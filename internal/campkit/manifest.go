package campkit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	TrustUnverified TrustStatus = "unverified"
	TrustVerified   TrustStatus = "verified"
	TrustRejected   TrustStatus = "rejected"
)

type TrustStatus string

type ArtifactIdentity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

type CapsuleGeneration struct {
	ID            string `json:"id"`
	Generation    uint64 `json:"generation"`
	ArchiveSHA256 string `json:"archiveSha256"`
}

type ImageIdentity struct {
	Reference string `json:"reference"`
	Digest    string `json:"digest"`
}

type TrustMetadata struct {
	Status     TrustStatus       `json:"status"`
	VerifiedBy string            `json:"verifiedBy,omitempty"`
	VerifiedAt time.Time         `json:"verifiedAt,omitempty"`
	Signature  *ArtifactIdentity `json:"signature,omitempty"`
}

type KitLineage struct {
	KitID               string    `json:"kitId"`
	ExportedAt          time.Time `json:"exportedAt"`
	SourceKitSHA256     string    `json:"sourceKitSha256,omitempty"`
	ParentKitSHA256     string    `json:"parentKitSha256,omitempty"`
	SourceGenerationSHA string    `json:"sourceGenerationSha256"`
}

type Manifest struct {
	SchemaVersion          uint32             `json:"schemaVersion"`
	Camp                   ArtifactIdentity   `json:"camp"`
	Capsule                CapsuleGeneration  `json:"capsule"`
	Runtime                ArtifactIdentity   `json:"runtime"`
	Tools                  []ArtifactIdentity `json:"tools"`
	WorkspaceImages        []ImageIdentity    `json:"workspaceImages"`
	RoomImage              ImageIdentity      `json:"roomImage"`
	DevPodProvider         ArtifactIdentity   `json:"devpodProvider"`
	SupportedArchitectures []string           `json:"supportedArchitectures"`
	Trust                  TrustMetadata      `json:"trust"`
	Lineage                KitLineage         `json:"lineage"`
}

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)
var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func Validate(m Manifest) error {
	if m.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schema version %d", m.SchemaVersion)
	}
	if err := validateArtifact("camp", m.Camp); err != nil {
		return err
	}
	if m.Capsule.ID == "" || !safeID.MatchString(m.Capsule.ID) {
		return fmt.Errorf("invalid capsule id")
	}
	if m.Capsule.Generation == 0 || !hex64.MatchString(m.Capsule.ArchiveSHA256) {
		return fmt.Errorf("invalid capsule generation")
	}
	if err := validateArtifact("runtime", m.Runtime); err != nil {
		return err
	}
	if len(m.Tools) == 0 {
		return fmt.Errorf("tools closure is empty")
	}
	if err := validateUniqueArtifacts(m.Tools); err != nil {
		return err
	}
	if len(m.WorkspaceImages) == 0 {
		return fmt.Errorf("workspace image closure is empty")
	}
	if err := validateUniqueImages(m.WorkspaceImages); err != nil {
		return err
	}
	if err := validateImage("room image", m.RoomImage); err != nil {
		return err
	}
	if err := validateArtifact("devpod provider", m.DevPodProvider); err != nil {
		return err
	}
	if len(m.SupportedArchitectures) == 0 {
		return fmt.Errorf("architectures are empty")
	}
	seen := map[string]bool{}
	for _, arch := range m.SupportedArchitectures {
		if arch != "amd64" && arch != "arm64" {
			return fmt.Errorf("unsupported architecture %q", arch)
		}
		if seen[arch] {
			return fmt.Errorf("duplicate architecture %q", arch)
		}
		seen[arch] = true
	}
	if err := validateTrust(m.Trust); err != nil {
		return err
	}
	if m.Lineage.KitID == "" || !safeID.MatchString(m.Lineage.KitID) || m.Lineage.ExportedAt.IsZero() || m.Lineage.ExportedAt.After(time.Now().UTC()) || !hex64.MatchString(m.Lineage.SourceGenerationSHA) {
		return fmt.Errorf("invalid lineage")
	}
	if m.Lineage.SourceKitSHA256 != "" && !hex64.MatchString(m.Lineage.SourceKitSHA256) {
		return fmt.Errorf("invalid source kit digest")
	}
	if m.Lineage.ParentKitSHA256 != "" && !hex64.MatchString(m.Lineage.ParentKitSHA256) {
		return fmt.Errorf("invalid parent kit digest")
	}
	return nil
}

func validateArtifact(label string, a ArtifactIdentity) error {
	if a.Name == "" || a.Version == "" || !hex64.MatchString(a.SHA256) {
		return fmt.Errorf("invalid %s identity", label)
	}
	return nil
}
func validateUniqueArtifacts(as []ArtifactIdentity) error {
	seen := map[string]bool{}
	for _, a := range as {
		if err := validateArtifact("artifact", a); err != nil {
			return err
		}
		if seen[a.Name] {
			return fmt.Errorf("duplicate tool %q", a.Name)
		}
		seen[a.Name] = true
	}
	return nil
}
func validateImage(label string, i ImageIdentity) error {
	if i.Digest != "sha256:"+strings.TrimPrefix(i.Digest, "sha256:") || len(i.Digest) != 71 || !hex64.MatchString(strings.TrimPrefix(i.Digest, "sha256:")) || !strings.Contains(i.Reference, "@"+i.Digest) {
		return fmt.Errorf("invalid %s", label)
	}
	if strings.Contains(i.Reference, ":latest") || !strings.Contains(i.Reference, "@sha256:") {
		return fmt.Errorf("mutable %s reference", label)
	}
	return nil
}
func validateUniqueImages(is []ImageIdentity) error {
	seen := map[string]bool{}
	for _, i := range is {
		if err := validateImage("workspace image", i); err != nil {
			return err
		}
		if seen[i.Reference] {
			return fmt.Errorf("duplicate image")
		}
		seen[i.Reference] = true
	}
	return nil
}
func validateTrust(t TrustMetadata) error {
	switch t.Status {
	case TrustUnverified:
		if t.VerifiedBy != "" || !t.VerifiedAt.IsZero() || t.Signature != nil {
			return fmt.Errorf("unverified trust has evidence")
		}
	case TrustVerified:
		if t.VerifiedBy == "" || t.VerifiedAt.IsZero() || t.Signature == nil {
			return fmt.Errorf("verified trust is incomplete")
		}
		if !strings.HasPrefix(t.VerifiedBy, "sha256:") || !hex64.MatchString(strings.TrimPrefix(t.VerifiedBy, "sha256:")) {
			return fmt.Errorf("invalid verifier")
		}
		if err := validateArtifact("signature", *t.Signature); err != nil {
			return err
		}
	case TrustRejected:
	default:
		return fmt.Errorf("unknown trust status %q", t.Status)
	}
	return nil
}

func MarshalCanonical(m Manifest) ([]byte, error) {
	if err := Validate(m); err != nil {
		return nil, err
	}
	c := m
	c.Tools = append([]ArtifactIdentity(nil), m.Tools...)
	sort.Slice(c.Tools, func(i, j int) bool { return c.Tools[i].Name < c.Tools[j].Name })
	c.WorkspaceImages = append([]ImageIdentity(nil), m.WorkspaceImages...)
	sort.Slice(c.WorkspaceImages, func(i, j int) bool { return c.WorkspaceImages[i].Reference < c.WorkspaceImages[j].Reference })
	c.SupportedArchitectures = append([]string(nil), m.SupportedArchitectures...)
	sort.Strings(c.SupportedArchitectures)
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func DecodeCanonical(b []byte) (Manifest, error) {
	var m Manifest
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, err
	}
	var extra any
	if err := d.Decode(&extra); err == nil {
		return m, fmt.Errorf("trailing data")
	} else if err != io.EOF {
		return m, fmt.Errorf("trailing data")
	}
	canonical, err := MarshalCanonical(m)
	if err != nil {
		return m, err
	}
	if !bytes.Equal(b, canonical) {
		return m, fmt.Errorf("document is not canonical")
	}
	return m, nil
}
