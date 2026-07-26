package campkit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ManifestSchemaVersion uint32 = 2

	maxManifestBytes = 4 << 20
	maxPayloads      = 256
	maxImages        = 8192
	maxPlatforms     = 2
	maxStringBytes   = 4 << 10
	maxPathBytes     = 255
)

type PayloadRole string

const (
	PayloadCamp               PayloadRole = "camp"
	PayloadGenerationArchive  PayloadRole = "generation-archive"
	PayloadGenerationMetadata PayloadRole = "generation-metadata"
	PayloadRuntime            PayloadRole = "runtime"
	PayloadTool               PayloadRole = "tool"
	PayloadDevPodProvider     PayloadRole = "devpod-provider"
	PayloadTrustEvidence      PayloadRole = "trust-evidence"
)

type ImageRole string

const (
	ImageRoom      ImageRole = "room"
	ImageWorkspace ImageRole = "workspace"
)

type TrustStatus string

const (
	TrustUnverified TrustStatus = "unverified"
	TrustVerified   TrustStatus = "verified"
	TrustRejected   TrustStatus = "rejected"
)

type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
}

type GenerationRef struct {
	Generation    uint64 `json:"generation"`
	ArchiveSHA256 string `json:"archiveSha256"`
}

type GenerationIdentity struct {
	Capsule      string         `json:"capsule"`
	Branch       string         `json:"branch"`
	Ref          GenerationRef  `json:"ref"`
	Parent       *GenerationRef `json:"parent,omitempty"`
	ArchivePath  string         `json:"archivePath"`
	MetadataPath string         `json:"metadataPath"`
}

type PayloadIdentity struct {
	Role             PayloadRole `json:"role"`
	Name             string      `json:"name"`
	Version          string      `json:"version,omitempty"`
	Repository       string      `json:"repository,omitempty"`
	Commit           string      `json:"commit,omitempty"`
	Platform         *Platform   `json:"platform,omitempty"`
	Path             string      `json:"path"`
	Size             int64       `json:"size"`
	SHA256           string      `json:"sha256"`
	ExecutableSHA256 string      `json:"executableSha256,omitempty"`
	MediaType        string      `json:"mediaType"`
}

type ImageIdentity struct {
	Role      ImageRole `json:"role"`
	Reference string    `json:"reference"`
	Digest    string    `json:"digest"`
	Platform  Platform  `json:"platform"`
}

type TrustMetadata struct {
	Status       TrustStatus `json:"status"`
	Verifier     string      `json:"verifier,omitempty"`
	VerifiedAt   *time.Time  `json:"verifiedAt,omitempty"`
	EvidencePath string      `json:"evidencePath,omitempty"`
}

type KitLineage struct {
	KitID            string        `json:"kitId"`
	ExportedAt       time.Time     `json:"exportedAt"`
	SourceGeneration GenerationRef `json:"sourceGeneration"`
	ParentKitSHA256  string        `json:"parentKitSha256,omitempty"`
}

type Manifest struct {
	SchemaVersion      uint32             `json:"schemaVersion"`
	Generation         GenerationIdentity `json:"generation"`
	SupportedPlatforms []Platform         `json:"supportedPlatforms"`
	Payloads           []PayloadIdentity  `json:"payloads"`
	Images             []ImageIdentity    `json:"images"`
	Trust              TrustMetadata      `json:"trust"`
	Lineage            KitLineage         `json:"lineage"`
}

// UnsupportedSchemaError identifies a well-formed manifest whose schema is not
// implemented by this decoder.
type UnsupportedSchemaError struct {
	Version uint32
}

func (e *UnsupportedSchemaError) Error() string {
	return fmt.Sprintf("unsupported schema version %d", e.Version)
}

var (
	hex64       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hex40       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	safeSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	ociName     = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]+)?(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$`)
)

func Validate(m Manifest) error {
	if m.SchemaVersion != ManifestSchemaVersion {
		return &UnsupportedSchemaError{Version: m.SchemaVersion}
	}
	if err := validateGeneration(m.Generation); err != nil {
		return err
	}
	platforms, err := validatePlatforms(m.SupportedPlatforms)
	if err != nil {
		return err
	}
	if len(m.Payloads) == 0 || len(m.Payloads) > maxPayloads {
		return fmt.Errorf("payload count %d is outside 1..%d", len(m.Payloads), maxPayloads)
	}
	if len(m.Images) > maxImages {
		return fmt.Errorf("image count %d exceeds %d", len(m.Images), maxImages)
	}
	if err := validateLineage(m.Lineage, m.Generation.Ref); err != nil {
		return err
	}
	payloadState, err := validatePayloads(m.Payloads, platforms)
	if err != nil {
		return err
	}
	if err := validateGenerationBindings(m.Generation, payloadState); err != nil {
		return err
	}
	if err := validateExecutableClosure(payloadState, platforms); err != nil {
		return err
	}
	if err := validateImages(m.Images, platforms); err != nil {
		return err
	}
	if err := validateTrust(m.Trust, m.Lineage.ExportedAt, payloadState); err != nil {
		return err
	}
	return nil
}

func validateGeneration(g GenerationIdentity) error {
	if !validSegment(g.Capsule) {
		return fmt.Errorf("invalid capsule")
	}
	if !validSegment(g.Branch) {
		return fmt.Errorf("invalid branch")
	}
	if err := validateGenerationRef("generation", g.Ref); err != nil {
		return err
	}
	if g.Parent != nil {
		if err := validateGenerationRef("parent generation", *g.Parent); err != nil {
			return err
		}
		if g.Parent.Generation >= g.Ref.Generation {
			return fmt.Errorf("parent generation must precede generation")
		}
	}
	if err := validatePayloadPath(g.ArchivePath); err != nil {
		return fmt.Errorf("invalid generation archive path: %w", err)
	}
	if err := validatePayloadPath(g.MetadataPath); err != nil {
		return fmt.Errorf("invalid generation metadata path: %w", err)
	}
	if g.ArchivePath == g.MetadataPath {
		return fmt.Errorf("generation archive and metadata paths collide")
	}
	return nil
}

func validateGenerationRef(label string, ref GenerationRef) error {
	if ref.Generation == 0 || !hex64.MatchString(ref.ArchiveSHA256) {
		return fmt.Errorf("invalid %s", label)
	}
	return nil
}

func validateLineage(lineage KitLineage, source GenerationRef) error {
	if !validSegment(lineage.KitID) {
		return fmt.Errorf("invalid kit ID")
	}
	if lineage.ExportedAt.IsZero() {
		return fmt.Errorf("exportedAt is zero")
	}
	if lineage.SourceGeneration != source {
		return fmt.Errorf("source generation does not match generation")
	}
	if lineage.ParentKitSHA256 != "" && !hex64.MatchString(lineage.ParentKitSHA256) {
		return fmt.Errorf("invalid parent kit digest")
	}
	return nil
}

func validatePlatforms(values []Platform) (map[string]Platform, error) {
	if len(values) == 0 || len(values) > maxPlatforms {
		return nil, fmt.Errorf("platform count %d is outside 1..%d", len(values), maxPlatforms)
	}
	result := make(map[string]Platform, len(values))
	for _, p := range values {
		if err := validatePlatform(p); err != nil {
			return nil, err
		}
		key := platformKey(p)
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("duplicate platform %s", key)
		}
		result[key] = p
	}
	return result, nil
}

func validatePlatform(p Platform) error {
	if p.OS != "linux" || (p.Architecture != "amd64" && p.Architecture != "arm64") || p.Variant != "" {
		return fmt.Errorf("unsupported platform %q", platformKey(p))
	}
	return nil
}

type payloadValidationState struct {
	byRole       map[PayloadRole][]PayloadIdentity
	closureCount map[string]int
}

func validatePayloads(payloads []PayloadIdentity, platforms map[string]Platform) (payloadValidationState, error) {
	state := payloadValidationState{
		byRole:       make(map[PayloadRole][]PayloadIdentity),
		closureCount: make(map[string]int),
	}
	paths := make([]string, 0, len(payloads))
	semantic := make(map[string]struct{}, len(payloads))
	var total int64
	for _, p := range payloads {
		if err := validatePayload(p, platforms); err != nil {
			return state, err
		}
		if p.Size > math.MaxInt64-total {
			return state, fmt.Errorf("aggregate payload size overflows int64")
		}
		total += p.Size
		for _, other := range paths {
			if p.Path == other {
				return state, fmt.Errorf("duplicate payload path %q", p.Path)
			}
			if strings.HasPrefix(p.Path, other+"/") || strings.HasPrefix(other, p.Path+"/") {
				return state, fmt.Errorf("payload path ancestor collision %q and %q", p.Path, other)
			}
		}
		paths = append(paths, p.Path)
		pkey := "-"
		if p.Platform != nil {
			pkey = platformKey(*p.Platform)
		}
		identity := string(p.Role) + "\x00" + p.Name + "\x00" + pkey
		if _, exists := semantic[identity]; exists {
			return state, fmt.Errorf("duplicate payload identity for %s", identity)
		}
		semantic[identity] = struct{}{}
		state.byRole[p.Role] = append(state.byRole[p.Role], p)
		state.closureCount[string(p.Role)+"\x00"+p.Name+"\x00"+pkey]++
	}
	return state, nil
}

func validatePayload(p PayloadIdentity, platforms map[string]Platform) error {
	switch p.Role {
	case PayloadCamp, PayloadGenerationArchive, PayloadGenerationMetadata, PayloadRuntime, PayloadTool, PayloadDevPodProvider, PayloadTrustEvidence:
	default:
		return fmt.Errorf("unknown payload role %q", p.Role)
	}
	for label, value := range map[string]string{
		"payload name": p.Name, "version": p.Version, "repository": p.Repository,
		"commit": p.Commit, "media type": p.MediaType,
	} {
		if err := validateBoundedString(label, value); err != nil {
			return err
		}
	}
	if p.Name == "" || p.MediaType == "" {
		return fmt.Errorf("payload name and media type are required")
	}
	if err := validatePayloadPath(p.Path); err != nil {
		return fmt.Errorf("invalid payload path %q: %w", p.Path, err)
	}
	if p.Size <= 0 {
		return fmt.Errorf("payload %q size must be positive", p.Path)
	}
	if !hex64.MatchString(p.SHA256) {
		return fmt.Errorf("payload %q has invalid digest", p.Path)
	}
	independent := p.Role == PayloadGenerationArchive || p.Role == PayloadGenerationMetadata || p.Role == PayloadTrustEvidence
	if independent {
		if p.Platform != nil {
			return fmt.Errorf("platform-independent payload %q has a platform", p.Path)
		}
	} else {
		if p.Platform == nil {
			return fmt.Errorf("executable payload %q has no platform", p.Path)
		}
		if _, ok := platforms[platformKey(*p.Platform)]; !ok {
			return fmt.Errorf("payload %q uses an unsupported platform", p.Path)
		}
		if !hex64.MatchString(p.ExecutableSHA256) {
			return fmt.Errorf("payload %q has invalid executable digest", p.Path)
		}
	}
	if p.Role == PayloadTool {
		if p.Repository == "" || p.Version == "" || !hex40.MatchString(p.Commit) {
			return fmt.Errorf("tool %q has incomplete immutable identity", p.Name)
		}
		if p.Name != "devpod" && p.Name != "hauler" {
			return fmt.Errorf("unsupported tool %q", p.Name)
		}
		if p.Name == "devpod" && p.SHA256 != p.ExecutableSHA256 {
			return fmt.Errorf("DevPod executable digest must match asset digest")
		}
	}
	return nil
}

func validateGenerationBindings(g GenerationIdentity, state payloadValidationState) error {
	archives := state.byRole[PayloadGenerationArchive]
	if len(archives) != 1 {
		return fmt.Errorf("generation archive payload count is %d, want 1", len(archives))
	}
	if archives[0].Path != g.ArchivePath || archives[0].SHA256 != g.Ref.ArchiveSHA256 {
		return fmt.Errorf("generation archive payload does not bind generation")
	}
	metadata := state.byRole[PayloadGenerationMetadata]
	if len(metadata) != 1 {
		return fmt.Errorf("generation metadata payload count is %d, want 1", len(metadata))
	}
	if metadata[0].Path != g.MetadataPath {
		return fmt.Errorf("generation metadata payload does not bind metadata path")
	}
	return nil
}

func validateExecutableClosure(state payloadValidationState, platforms map[string]Platform) error {
	for key := range platforms {
		for _, required := range []struct {
			role PayloadRole
			name string
		}{
			{PayloadCamp, ""},
			{PayloadRuntime, ""},
			{PayloadDevPodProvider, ""},
			{PayloadTool, "devpod"},
			{PayloadTool, "hauler"},
		} {
			count := 0
			for _, p := range state.byRole[required.role] {
				if p.Platform != nil && platformKey(*p.Platform) == key && (required.name == "" || p.Name == required.name) {
					count++
				}
			}
			if count != 1 {
				return fmt.Errorf("%s %q closure count for %s is %d, want 1", required.role, required.name, key, count)
			}
		}
	}
	return nil
}

func validateImages(images []ImageIdentity, platforms map[string]Platform) error {
	semantic := make(map[string]struct{}, len(images))
	digestPlatforms := make(map[string]string, len(images))
	roomCount := make(map[string]int, len(platforms))
	for _, image := range images {
		if image.Role != ImageRoom && image.Role != ImageWorkspace {
			return fmt.Errorf("unknown image role %q", image.Role)
		}
		key := platformKey(image.Platform)
		if _, ok := platforms[key]; !ok {
			return fmt.Errorf("image uses unsupported platform %s", key)
		}
		if err := validateBoundedString("image reference", image.Reference); err != nil {
			return err
		}
		if err := validateOCIReference(image.Reference, image.Digest); err != nil {
			return err
		}
		identity := string(image.Role) + "\x00" + image.Reference + "\x00" + key
		if _, exists := semantic[identity]; exists {
			return fmt.Errorf("duplicate image identity")
		}
		semantic[identity] = struct{}{}
		if prior, exists := digestPlatforms[image.Reference]; exists && prior != key {
			return fmt.Errorf("image reference is reused across platforms and is not platform-specific")
		}
		digestPlatforms[image.Reference] = key
		if image.Role == ImageRoom {
			roomCount[key]++
		}
	}
	for key := range platforms {
		if roomCount[key] != 1 {
			return fmt.Errorf("Room image count for %s is %d, want 1", key, roomCount[key])
		}
	}
	return nil
}

func validateOCIReference(reference, digest string) error {
	if !strings.HasPrefix(digest, "sha256:") || !hex64.MatchString(strings.TrimPrefix(digest, "sha256:")) {
		return fmt.Errorf("invalid image digest")
	}
	if len(reference) == 0 || len(reference) > maxStringBytes || strings.ContainsAny(reference, " \t\r\n?#\\") ||
		strings.Contains(reference, "://") {
		return fmt.Errorf("invalid OCI reference")
	}
	at := strings.LastIndexByte(reference, '@')
	if at <= 0 || strings.Count(reference, "@") != 1 {
		return fmt.Errorf("OCI reference must contain exactly one digest separator")
	}
	name, suffix := reference[:at], reference[at+1:]
	if suffix != digest || !ociName.MatchString(name) {
		return fmt.Errorf("invalid digest-pinned OCI reference")
	}
	lastSlash := strings.LastIndexByte(name, '/')
	if strings.Contains(name[lastSlash+1:], ":") {
		return fmt.Errorf("OCI reference must not include a mutable tag")
	}
	return nil
}

func validateTrust(trust TrustMetadata, exportedAt time.Time, state payloadValidationState) error {
	if err := validateBoundedString("verifier", trust.Verifier); err != nil {
		return err
	}
	evidence := state.byRole[PayloadTrustEvidence]
	switch trust.Status {
	case TrustUnverified:
		if trust.Verifier != "" || trust.VerifiedAt != nil || trust.EvidencePath != "" || len(evidence) != 0 {
			return fmt.Errorf("unverified trust carries evidence")
		}
	case TrustVerified, TrustRejected:
		if !strings.HasPrefix(trust.Verifier, "sha256:") || !hex64.MatchString(strings.TrimPrefix(trust.Verifier, "sha256:")) {
			return fmt.Errorf("invalid verifier digest")
		}
		if trust.VerifiedAt == nil || trust.VerifiedAt.IsZero() || trust.VerifiedAt.After(exportedAt) {
			return fmt.Errorf("invalid verification time")
		}
		if len(evidence) != 1 || trust.EvidencePath == "" || evidence[0].Path != trust.EvidencePath {
			return fmt.Errorf("trust evidence is not exactly bound")
		}
	default:
		return fmt.Errorf("unknown trust status %q", trust.Status)
	}
	return nil
}

func validatePayloadPath(value string) error {
	if len(value) == 0 || len(value) > maxPathBytes || !utf8.ValidString(value) {
		return fmt.Errorf("path length is outside bounds")
	}
	for _, r := range value {
		if r > 0x7f || r < 0x20 || r == 0x7f {
			return fmt.Errorf("path must be printable ASCII")
		}
	}
	if value == "manifest.json" || !strings.HasPrefix(value, "payloads/") || strings.ContainsAny(value, `\?#`) {
		return fmt.Errorf("path is outside payloads or contains reserved syntax")
	}
	parts := strings.Split(value, "/")
	if len(parts) < 2 {
		return fmt.Errorf("path has no payload component")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("path has an unsafe component")
		}
		if strings.Contains(part, ":") {
			return fmt.Errorf("path has a scheme or drive prefix")
		}
	}
	return nil
}

func validSegment(value string) bool {
	return len(value) <= maxStringBytes && safeSegment.MatchString(value) && value != "." && value != ".."
}

func validateBoundedString(label, value string) error {
	if len(value) > maxStringBytes || !utf8.ValidString(value) {
		return fmt.Errorf("%s exceeds fixed bounds", label)
	}
	for _, r := range value {
		if r == 0 || r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s contains a control character", label)
		}
	}
	return nil
}

func platformKey(p Platform) string {
	return p.OS + "\x00" + p.Architecture + "\x00" + p.Variant
}

func MarshalCanonical(m Manifest) ([]byte, error) {
	if err := Validate(m); err != nil {
		return nil, err
	}
	c := deepCopyManifest(m)
	c.Lineage.ExportedAt = c.Lineage.ExportedAt.UTC()
	if c.Trust.VerifiedAt != nil {
		utc := c.Trust.VerifiedAt.UTC()
		c.Trust.VerifiedAt = &utc
	}
	sort.Slice(c.SupportedPlatforms, func(i, j int) bool {
		return platformKey(c.SupportedPlatforms[i]) < platformKey(c.SupportedPlatforms[j])
	})
	sort.Slice(c.Payloads, func(i, j int) bool {
		return payloadSortKey(c.Payloads[i]) < payloadSortKey(c.Payloads[j])
	})
	sort.Slice(c.Images, func(i, j int) bool {
		return imageSortKey(c.Images[i]) < imageSortKey(c.Images[j])
	})
	return json.Marshal(c)
}

func DecodeCanonical(body []byte) (Manifest, error) {
	var m Manifest
	if len(body) > maxManifestBytes {
		return m, fmt.Errorf("manifest exceeds maximum size of %d bytes", maxManifestBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return m, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return m, fmt.Errorf("trailing JSON value")
	} else if err != io.EOF {
		return m, fmt.Errorf("trailing data: %w", err)
	}
	canonical, err := MarshalCanonical(m)
	if err != nil {
		return m, err
	}
	if !bytes.Equal(body, canonical) {
		return m, fmt.Errorf("document is not canonical")
	}
	return m, nil
}

func deepCopyManifest(m Manifest) Manifest {
	c := m
	c.SupportedPlatforms = append([]Platform(nil), m.SupportedPlatforms...)
	c.Payloads = make([]PayloadIdentity, len(m.Payloads))
	for i := range m.Payloads {
		c.Payloads[i] = m.Payloads[i]
		if m.Payloads[i].Platform != nil {
			platform := *m.Payloads[i].Platform
			c.Payloads[i].Platform = &platform
		}
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

func payloadSortKey(p PayloadIdentity) string {
	key := "-"
	if p.Platform != nil {
		key = platformKey(*p.Platform)
	}
	return string(p.Role) + "\x00" + p.Name + "\x00" + key + "\x00" + p.Path
}

func imageSortKey(image ImageIdentity) string {
	return string(image.Role) + "\x00" + image.Reference + "\x00" + platformKey(image.Platform)
}
