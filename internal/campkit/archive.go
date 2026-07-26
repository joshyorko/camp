package campkit

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/klauspost/compress/zstd"
)

var (
	ErrArchiveFormat    = errors.New("invalid CampKit archive format")
	ErrArchiveLimit     = errors.New("CampKit archive limit exceeded")
	ErrUnsafeArchive    = errors.New("unsafe CampKit archive entry")
	ErrDigestMismatch   = errors.New("CampKit digest mismatch")
	ErrMetadataMismatch = errors.New("CampKit metadata mismatch")
	ErrOCIMismatch      = errors.New("CampKit OCI closure mismatch")
	ErrTrustRejected    = errors.New("CampKit trust rejected")
	ErrTrustUnsupported = errors.New("CampKit trust verifier unsupported")
)

type IntegrityStatus string

const (
	IntegrityNotVerified IntegrityStatus = "not-verified"
	IntegrityValid       IntegrityStatus = "valid"
)

type TrustResult string

const (
	TrustResultUnverified TrustResult = "unverified"
)

type OCIClosureStatus string

const (
	OCIClosureNotVerified OCIClosureStatus = "not-verified"
)

type TrustEvaluator interface {
	Evaluate(context.Context, Manifest, io.Reader) (TrustResult, error)
}

type PayloadVerification struct {
	Role      PayloadRole     `json:"role"`
	Name      string          `json:"name"`
	Size      int64           `json:"size"`
	SHA256    string          `json:"sha256"`
	Integrity IntegrityStatus `json:"integrity"`
}

type Verification struct {
	Manifest   Manifest              `json:"manifest"`
	Integrity  IntegrityStatus       `json:"integrity"`
	Trust      TrustResult           `json:"trust"`
	OCIClosure OCIClosureStatus      `json:"ociClosure"`
	Payloads   []PayloadVerification `json:"payloads"`
}

type ArchiveLimits struct {
	MaxCompressedBytes     int64
	MaxManifestBytes       int64
	MaxOuterEntries        int
	MaxOuterEntryBytes     int64
	MaxOuterExpandedBytes  int64
	MaxNestedEntries       int
	MaxNestedEntryBytes    int64
	MaxNestedExpandedBytes int64
	MaxNestedPathBytes     int
	MaxLinks               int
	MaxLinkTargetBytes     int
	MaxSymlinkDepth        int
	MaxCompressionRatio    int64
	CompressionRatioSlack  int64
	MaxTrustEvidenceBytes  int64
	MaxDescriptorBytes     int64
}

func DefaultArchiveLimits() ArchiveLimits {
	return ArchiveLimits{
		MaxCompressedBytes:     256 << 30,
		MaxManifestBytes:       maxManifestBytes,
		MaxOuterEntries:        maxPayloads + 1,
		MaxOuterEntryBytes:     128 << 30,
		MaxOuterExpandedBytes:  256 << 30,
		MaxNestedEntries:       1_000_000,
		MaxNestedEntryBytes:    128 << 30,
		MaxNestedExpandedBytes: 512 << 30,
		MaxNestedPathBytes:     1024,
		MaxLinks:               65_536,
		MaxLinkTargetBytes:     255,
		MaxSymlinkDepth:        40,
		MaxCompressionRatio:    200,
		CompressionRatioSlack:  64 << 20,
		MaxTrustEvidenceBytes:  16 << 20,
		MaxDescriptorBytes:     4 << 20,
	}
}

type Inspection struct {
	Manifest  Manifest        `json:"manifest"`
	Integrity IntegrityStatus `json:"integrity"`
}

func Inspect(r io.Reader, limits ArchiveLimits) (Inspection, error) {
	if err := validateReaderLimits(limits); err != nil {
		return Inspection{}, err
	}
	compressed := &countingReader{reader: r, limit: limits.MaxCompressedBytes}
	frame := newZstdFrameReader(compressed, true)
	decoder, err := zstd.NewReader(
		frame,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderLowmem(true),
		zstd.WithDecoderMaxMemory(uint64(limits.MaxOuterExpandedBytes)),
		zstd.WithDecoderMaxWindow(uint64(limits.MaxOuterEntryBytes)),
	)
	if err != nil {
		return Inspection{}, fmt.Errorf("open CampKit zstd stream: %w: %w", err, ErrArchiveFormat)
	}
	defer decoder.Close()
	reader := tar.NewReader(newRatioReader(decoder, compressed, limits))
	header, err := reader.Next()
	if err != nil {
		return Inspection{}, fmt.Errorf("read CampKit manifest header: %w: %w", err, ErrArchiveFormat)
	}
	if header.Name != "manifest.json" {
		return Inspection{}, fmt.Errorf("first entry is %q: %w", header.Name, ErrArchiveFormat)
	}
	if err := validateOuterHeader(header); err != nil {
		return Inspection{}, err
	}
	if header.Size > limits.MaxManifestBytes {
		return Inspection{}, &LimitError{Resource: "manifest bytes", Limit: limits.MaxManifestBytes, Observed: header.Size}
	}
	body, err := readExactBounded(reader, header.Size, limits.MaxManifestBytes)
	if err != nil {
		return Inspection{}, fmt.Errorf("read manifest.json: %w", err)
	}
	manifest, err := DecodeCanonical(body)
	if err != nil {
		return Inspection{}, fmt.Errorf("decode manifest.json: %w: %w", err, ErrArchiveFormat)
	}
	return Inspection{Manifest: manifest, Integrity: IntegrityNotVerified}, nil
}

func Verify(ctx context.Context, reader io.Reader, limits ArchiveLimits, evaluator TrustEvaluator) (Verification, error) {
	if err := validateReaderLimits(limits); err != nil {
		return Verification{}, err
	}
	compressed := &countingReader{reader: reader, limit: limits.MaxCompressedBytes}
	frame := newZstdFrameReader(compressed, true)
	decoder, err := zstd.NewReader(
		frame,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderLowmem(true),
		zstd.WithDecoderMaxMemory(uint64(limits.MaxOuterExpandedBytes)),
		zstd.WithDecoderMaxWindow(uint64(limits.MaxOuterEntryBytes)),
	)
	if err != nil {
		return Verification{}, fmt.Errorf("open CampKit zstd stream: %w: %w", err, ErrArchiveFormat)
	}
	defer decoder.Close()

	readerWithLimits := newRatioReader(decoder, compressed, limits)
	tarReader := tar.NewReader(readerWithLimits)

	header, err := tarReader.Next()
	if err != nil {
		return Verification{}, fmt.Errorf("read CampKit manifest header: %w: %w", err, ErrArchiveFormat)
	}
	if header.Name != "manifest.json" {
		return Verification{}, fmt.Errorf("first entry is %q: %w", header.Name, ErrArchiveFormat)
	}
	if err := validateOuterHeader(header); err != nil {
		return Verification{}, err
	}
	if header.Size > limits.MaxManifestBytes {
		return Verification{}, &LimitError{Resource: "manifest bytes", Limit: limits.MaxManifestBytes, Observed: header.Size}
	}
	body, err := readExactBounded(tarReader, header.Size, limits.MaxManifestBytes)
	if err != nil {
		return Verification{}, fmt.Errorf("read manifest.json: %w", err)
	}
	manifest, err := DecodeCanonical(body)
	if err != nil {
		return Verification{}, fmt.Errorf("decode manifest.json: %w: %w", err, ErrArchiveFormat)
	}

	payloads := make([]PayloadIdentity, len(manifest.Payloads))
	copy(payloads, manifest.Payloads)
	if len(payloads)+1 > limits.MaxOuterEntries {
		return Verification{}, &LimitError{Resource: "outer entries", Limit: int64(limits.MaxOuterEntries), Observed: int64(len(payloads) + 1)}
	}
	paths := make([]string, 0, len(payloads))
	byPath := make(map[string]PayloadIdentity, len(payloads))
	for _, payload := range payloads {
		paths = append(paths, payload.Path)
		byPath[payload.Path] = payload
	}
	sort.Strings(paths)

	verifiedPayloads := make([]PayloadVerification, 0, len(payloads))
	seen := make(map[string]struct{}, len(payloads))
	var trustEvidence []byte
	for expected := 0; ; expected++ {
		entry, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Verification{}, fmt.Errorf("read CampKit payload entry: %w: %w", err, ErrArchiveFormat)
		}
		if _, exists := seen[entry.Name]; exists {
			return Verification{}, fmt.Errorf("payload %q repeated: %w", entry.Name, ErrArchiveFormat)
		}
		if expected >= len(paths) {
			return Verification{}, fmt.Errorf("unexpected payload %q: %w", entry.Name, ErrArchiveFormat)
		}
		if entry.Name != paths[expected] {
			return Verification{}, fmt.Errorf("payload order mismatch at %q: %w", entry.Name, ErrArchiveFormat)
		}
		seen[entry.Name] = struct{}{}
		if err := validateOuterHeader(entry); err != nil {
			return Verification{}, err
		}
		payload, ok := byPath[entry.Name]
		if !ok {
			return Verification{}, fmt.Errorf("payload %q not declared in manifest: %w", entry.Name, ErrArchiveFormat)
		}
		if entry.Size != payload.Size {
			return Verification{}, fmt.Errorf("%q payload size %d != manifest %d: %w", entry.Name, entry.Size, payload.Size, ErrDigestMismatch)
		}
		var body []byte
		digest := ""
		switch payload.Role {
		case PayloadGenerationMetadata:
			body, err = readExactBounded(tarReader, entry.Size, limits.MaxOuterEntryBytes)
			if err != nil {
				return Verification{}, fmt.Errorf("read payload %q: %w", entry.Name, err)
			}
			digest = sha256Hex(body)
		case PayloadTrustEvidence:
			body, err = readExactBounded(tarReader, entry.Size, limits.MaxTrustEvidenceBytes)
			if err != nil {
				return Verification{}, fmt.Errorf("read trust evidence %q: %w", entry.Name, err)
			}
			trustEvidence = body
			digest = sha256Hex(body)
		default:
			digest, err = sha256HexFromReader(tarReader, entry.Size, limits.MaxOuterEntryBytes)
			if err != nil {
				return Verification{}, fmt.Errorf("read payload %q: %w", entry.Name, err)
			}
		}
		if digest != payload.SHA256 {
			return Verification{}, fmt.Errorf("%q digest mismatch: %w", entry.Name, ErrDigestMismatch)
		}
		if payload.Role == PayloadGenerationMetadata && !metadataMatchesGeneration(body, manifest.Generation) {
			return Verification{}, ErrMetadataMismatch
		}
		verifiedPayloads = append(verifiedPayloads, PayloadVerification{
			Role:      payload.Role,
			Name:      payload.Name,
			Size:      payload.Size,
			SHA256:    payload.SHA256,
			Integrity: IntegrityValid,
		})
	}
	if len(verifiedPayloads) != len(payloads) {
		return Verification{}, fmt.Errorf("missing payload entries: expected %d got %d: %w", len(payloads), len(verifiedPayloads), ErrArchiveFormat)
	}
	var trailer [1]byte
	for i := 0; i < 8; i++ {
		n, err := readerWithLimits.Read(trailer[:])
		if n > 0 {
			continue
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return Verification{}, fmt.Errorf("read CampKit zstd tail: %w", err)
		}
	}
	if err := frame.requirePhysicalEOF(); err != nil {
		return Verification{}, fmt.Errorf("read CampKit end-of-frame: %w", err)
	}

	verification := Verification{
		Manifest:   manifest,
		Integrity:  IntegrityValid,
		Trust:      TrustResultUnverified,
		OCIClosure: OCIClosureNotVerified,
		Payloads:   verifiedPayloads,
	}
	switch manifest.Trust.Status {
	case TrustUnverified:
	case TrustVerified, TrustRejected:
		if evaluator == nil {
			return verification, ErrTrustUnsupported
		}
		if len(trustEvidence) == 0 {
			return verification, ErrTrustUnsupported
		}
		trust, trustErr := evaluator.Evaluate(ctx, manifest, bytes.NewReader(trustEvidence))
		if trustErr != nil {
			return verification, trustErr
		}
		verification.Trust = trust
	default:
		return verification, fmt.Errorf("unsupported trust status %q", manifest.Trust.Status)
	}

	return verification, nil
}

func metadataMatchesGeneration(body []byte, generation GenerationIdentity) bool {
	var metadata struct {
		Capsule    string               `json:"capsule"`
		Lineage    domain.Lineage       `json:"lineage"`
		Generation domain.GenerationRef `json:"generation"`
		Verified   domain.Verification  `json:"verified"`
	}
	if err := json.Unmarshal(body, &metadata); err != nil {
		return false
	}
	return metadata.Capsule == generation.Capsule &&
		metadata.Generation.Generation == generation.Ref.Generation &&
		metadata.Generation.ArchiveSHA256 == generation.Ref.ArchiveSHA256 &&
		metadata.Lineage.Branch == generation.Branch &&
		metadata.Verified.RemoteBytesVerified
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func sha256HexFromReader(reader io.Reader, size, limit int64) (string, error) {
	if size < 0 || size > limit {
		return "", &LimitError{Resource: "entry bytes", Limit: limit, Observed: size}
	}
	h := sha256.New()
	body := make([]byte, min(64*1024, size))
	remaining := size
	for remaining > 0 {
		chunk := min(len(body), int(remaining))
		n, err := io.ReadFull(reader, body[:chunk])
		if n > 0 {
			h.Write(body[:n])
			remaining -= int64(n)
		}
		if err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return "", io.ErrUnexpectedEOF
			}
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type LimitError struct {
	Resource string
	Limit    int64
	Observed int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("%s observed %d bytes, limit %d", e.Resource, e.Observed, e.Limit)
}

func (e *LimitError) Unwrap() error { return ErrArchiveLimit }

func validateReaderLimits(limits ArchiveLimits) error {
	values := []int64{
		limits.MaxCompressedBytes,
		limits.MaxManifestBytes,
		limits.MaxOuterEntryBytes,
		limits.MaxOuterExpandedBytes,
		limits.MaxNestedEntryBytes,
		limits.MaxNestedExpandedBytes,
		limits.MaxCompressionRatio,
		limits.CompressionRatioSlack,
		limits.MaxTrustEvidenceBytes,
		limits.MaxDescriptorBytes,
	}
	for _, value := range values {
		if value <= 0 {
			return fmt.Errorf("archive limits must be positive: %w", ErrArchiveLimit)
		}
	}
	if limits.MaxOuterEntries <= 0 || limits.MaxNestedEntries <= 0 ||
		limits.MaxNestedPathBytes <= 0 || limits.MaxLinks <= 0 ||
		limits.MaxLinkTargetBytes <= 0 || limits.MaxSymlinkDepth <= 0 {
		return fmt.Errorf("archive count limits must be positive: %w", ErrArchiveLimit)
	}
	if limits.MaxManifestBytes > maxManifestBytes || limits.MaxOuterEntries > maxPayloads+1 {
		return fmt.Errorf("reader limits exceed schema bounds: %w", ErrArchiveLimit)
	}
	return nil
}

func validateOuterHeader(header *tar.Header) error {
	epoch := time.Unix(0, 0).UTC()
	if header.Format != tar.FormatUSTAR ||
		header.Typeflag != tar.TypeReg ||
		header.Mode != 0o444 ||
		header.Uid != 0 ||
		header.Gid != 0 ||
		header.Uname != "" ||
		header.Gname != "" ||
		!header.ModTime.Equal(epoch) ||
		!header.AccessTime.IsZero() ||
		!header.ChangeTime.IsZero() ||
		header.Devmajor != 0 ||
		header.Devminor != 0 ||
		header.Linkname != "" ||
		len(header.PAXRecords) != 0 ||
		len(header.Xattrs) != 0 {
		return fmt.Errorf("entry %q has a non-deterministic ustar header: %w", header.Name, ErrUnsafeArchive)
	}
	return nil
}

func readExactBounded(reader io.Reader, size, limit int64) ([]byte, error) {
	if size < 0 || size > limit {
		return nil, &LimitError{Resource: "entry bytes", Limit: limit, Observed: size}
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, err
	}
	return body, nil
}

type countingReader struct {
	reader io.Reader
	count  int64
	limit  int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	remaining := r.limit - r.count
	if remaining <= 0 {
		var probe [1]byte
		n, err := r.reader.Read(probe[:])
		if n > 0 {
			return 0, &LimitError{Resource: "compressed CampKit", Limit: r.limit, Observed: r.count + int64(n)}
		}
		return 0, err
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := r.reader.Read(p)
	r.count += int64(n)
	return n, err
}

type ratioReader struct {
	reader     io.Reader
	compressed *countingReader
	limits     ArchiveLimits
	expanded   int64
}

func newRatioReader(reader io.Reader, compressed *countingReader, limits ArchiveLimits) *ratioReader {
	return &ratioReader{reader: reader, compressed: compressed, limits: limits}
}

func (r *ratioReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.expanded += int64(n)
	allowed := r.limits.CompressionRatioSlack
	if r.compressed.count > (1<<63-1-allowed)/r.limits.MaxCompressionRatio {
		allowed = 1<<63 - 1
	} else {
		allowed += r.compressed.count * r.limits.MaxCompressionRatio
	}
	if r.expanded > r.limits.MaxOuterExpandedBytes || r.expanded > allowed {
		return n, &LimitError{Resource: "expanded CampKit", Limit: min(r.limits.MaxOuterExpandedBytes, allowed), Observed: r.expanded}
	}
	return n, err
}

type zstdFrameReader struct {
	reader        io.Reader
	exactOuter    bool
	state         frameReadState
	segment       []byte
	segmentOffset int
	blockBytes    int64
	hasChecksum   bool
	done          bool
}

type frameReadState uint8

const (
	frameMagic frameReadState = iota
	frameDescriptor
	frameWindow
	frameBlockHeader
	frameBlockData
	frameChecksum
	frameDone
)

func newZstdFrameReader(reader io.Reader, exactOuter bool) *zstdFrameReader {
	return &zstdFrameReader{reader: reader, exactOuter: exactOuter, state: frameMagic, segment: make([]byte, 4)}
}

func (r *zstdFrameReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if r.state == frameDone {
			r.done = true
			return 0, io.EOF
		}
		if r.state == frameBlockData {
			if r.blockBytes == 0 {
				if len(r.segment) == 1 {
					r.finishBlock()
				} else {
					r.state = frameBlockHeader
					r.segment = make([]byte, 3)
					r.segmentOffset = 0
				}
				continue
			}
			if int64(len(p)) > r.blockBytes {
				p = p[:r.blockBytes]
			}
			n, err := r.reader.Read(p)
			r.blockBytes -= int64(n)
			return n, err
		}
		remaining := len(r.segment) - r.segmentOffset
		n, err := r.reader.Read(p[:min(len(p), remaining)])
		if n > 0 {
			copy(r.segment[r.segmentOffset:], p[:n])
			r.segmentOffset += n
		}
		if err != nil {
			return n, err
		}
		if r.segmentOffset != len(r.segment) {
			return n, nil
		}
		if err := r.advance(); err != nil {
			return n, err
		}
		return n, nil
	}
}

func (r *zstdFrameReader) advance() error {
	switch r.state {
	case frameMagic:
		if !bytes.Equal(r.segment, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
			return fmt.Errorf("invalid zstd frame magic: %w", ErrArchiveFormat)
		}
		r.state = frameDescriptor
		r.segment = make([]byte, 1)
	case frameDescriptor:
		descriptor := r.segment[0]
		if descriptor&0x18 != 0 {
			return fmt.Errorf("zstd frame uses reserved header bits: %w", ErrArchiveFormat)
		}
		singleSegment := descriptor&0x20 != 0
		contentSizeFlag := descriptor >> 6
		dictionaryFlag := descriptor & 0x03
		r.hasChecksum = descriptor&0x04 != 0
		if r.exactOuter && (dictionaryFlag != 0 || !r.hasChecksum) {
			return fmt.Errorf("zstd frame differs from deterministic CampKit contract: %w", ErrArchiveFormat)
		}
		dictionaryBytes := []int{0, 1, 2, 4}[dictionaryFlag]
		contentSizeBytes := []int{0, 2, 4, 8}[contentSizeFlag]
		if singleSegment && contentSizeFlag == 0 {
			contentSizeBytes = 1
		}
		r.state = frameWindow
		headerBytes := dictionaryBytes + contentSizeBytes
		if !singleSegment {
			headerBytes++
		}
		r.segment = make([]byte, headerBytes)
	case frameWindow:
		r.state = frameBlockHeader
		r.segment = make([]byte, 3)
	case frameBlockHeader:
		value := uint32(r.segment[0]) | uint32(r.segment[1])<<8 | uint32(r.segment[2])<<16
		last := value&1 != 0
		blockType := (value >> 1) & 0x03
		size := int64(value >> 3)
		if blockType == 3 {
			return fmt.Errorf("reserved zstd block type: %w", ErrArchiveFormat)
		}
		if blockType == 1 {
			size = 1
		}
		r.blockBytes = size
		if last {
			if size == 0 {
				if r.hasChecksum {
					r.state = frameChecksum
					r.segment = make([]byte, 4)
				} else {
					r.state = frameDone
					r.segment = nil
				}
			} else {
				r.state = frameBlockData
				if r.hasChecksum {
					// The final transition is selected when the payload is exhausted.
					r.segment = []byte{1}
				} else {
					r.segment = []byte{0}
				}
			}
		} else {
			r.state = frameBlockData
			r.segment = nil
		}
	case frameChecksum:
		r.state = frameDone
		r.segment = nil
	default:
		return fmt.Errorf("invalid zstd frame state: %w", ErrArchiveFormat)
	}
	r.segmentOffset = 0
	return nil
}

func (r *zstdFrameReader) finishBlock() {
	if len(r.segment) == 1 {
		if r.segment[0] == 1 {
			r.state = frameChecksum
			r.segment = make([]byte, 4)
		} else {
			r.state = frameDone
			r.segment = nil
		}
		r.segmentOffset = 0
	}
}

func (r *zstdFrameReader) requirePhysicalEOF() error {
	if !r.done {
		return fmt.Errorf("zstd frame did not terminate: %w", ErrArchiveFormat)
	}
	var probe [1]byte
	n, err := r.reader.Read(probe[:])
	if n != 0 {
		return fmt.Errorf("bytes follow the single zstd frame: %w", ErrArchiveFormat)
	}
	if err != io.EOF {
		if err == nil {
			return fmt.Errorf("input did not terminate after zstd frame: %w", ErrArchiveFormat)
		}
		return err
	}
	return nil
}

func (r *zstdFrameReader) blockDataReadComplete() {
	if r.state == frameBlockData && r.blockBytes == 0 {
		r.finishBlock()
	}
}

func parseLE32(body []byte) uint32 { return binary.LittleEndian.Uint32(body) }
