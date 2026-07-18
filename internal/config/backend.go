package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

type BackendKind string

const (
	BackendFile BackendKind = "file"
	BackendS3   BackendKind = "s3"
)

type S3Values struct {
	Endpoint  string `json:"endpoint" yaml:"endpoint"`
	Region    string `json:"region" yaml:"region"`
	PathStyle bool   `json:"pathStyle" yaml:"pathStyle"`
	Insecure  bool   `json:"insecure" yaml:"insecure"`
}

type S3Backend struct {
	Endpoint  string `json:"endpoint" yaml:"endpoint"`
	Region    string `json:"region" yaml:"region"`
	Bucket    string `json:"bucket" yaml:"bucket"`
	Prefix    string `json:"prefix,omitempty" yaml:"prefix,omitempty"`
	PathStyle bool   `json:"pathStyle" yaml:"pathStyle"`
	Insecure  bool   `json:"insecure" yaml:"insecure"`
}

type Backend struct {
	Kind         BackendKind  `json:"kind" yaml:"kind"`
	SanitizedURL string       `json:"url" yaml:"url"`
	Fingerprint  string       `json:"fingerprint" yaml:"fingerprint"`
	File         *FileBackend `json:"file,omitempty" yaml:"file,omitempty"`
	S3           *S3Backend   `json:"s3,omitempty" yaml:"s3,omitempty"`
}

func ResolveBackend(raw string, values S3Values) (Backend, error) {
	if strings.HasPrefix(raw, "file:") {
		file, err := ResolveFileBackend(raw)
		if err != nil {
			return Backend{}, err
		}
		return Backend{Kind: BackendFile, SanitizedURL: file.SanitizedURL, Fingerprint: file.Fingerprint, File: &file}, nil
	}
	if strings.TrimSpace(raw) != raw || !strings.HasPrefix(raw, "s3://") || strings.Contains(raw, "%") {
		return Backend{}, errors.New("backend must be a strict file:/// or s3:// URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return Backend{}, fmt.Errorf("parse S3 backend: %w", err)
	}
	if parsed.Scheme != "s3" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || strings.ContainsAny(parsed.Host, "/\\") {
		return Backend{}, errors.New("S3 backend URL may contain only bucket and prefix")
	}
	prefix := strings.Trim(parsed.Path, "/")
	if parsed.Path != "" && parsed.Path != "/" && (prefix == "" || hasUnsafeSegment(prefix)) {
		return Backend{}, errors.New("S3 backend prefix is unsafe")
	}
	endpoint, err := url.Parse(values.Endpoint)
	if err != nil || endpoint.Scheme != "http" && endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return Backend{}, errors.New("S3 endpoint must be a credential-free HTTP(S) origin")
	}
	if values.Insecure != (endpoint.Scheme == "http") {
		return Backend{}, errors.New("S3 HTTP endpoints require explicit insecure policy and HTTPS endpoints forbid it")
	}
	if !validBucket(parsed.Host) {
		return Backend{}, errors.New("S3 bucket must be a DNS-compatible name")
	}
	region := strings.TrimSpace(values.Region)
	if region == "" || region != values.Region {
		return Backend{}, errors.New("S3 region is required")
	}
	sanitized := "s3://" + parsed.Host
	if prefix != "" {
		sanitized += "/" + prefix
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{"s3", endpoint.String(), region, parsed.Host, prefix, fmt.Sprint(values.PathStyle), fmt.Sprint(values.Insecure)}, "\x00")))
	s3 := S3Backend{Endpoint: endpoint.String(), Region: region, Bucket: parsed.Host, Prefix: prefix, PathStyle: values.PathStyle, Insecure: values.Insecure}
	return Backend{Kind: BackendS3, SanitizedURL: sanitized, Fingerprint: hex.EncodeToString(digest[:]), S3: &s3}, nil
}

func validBucket(bucket string) bool {
	if len(bucket) < 3 || len(bucket) > 63 || strings.Contains(bucket, "..") {
		return false
	}
	if net.ParseIP(bucket) != nil {
		return false
	}
	for index, char := range bucket {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || (char == '-' || char == '.') && index > 0 && index < len(bucket)-1 {
			continue
		}
		return false
	}
	return true
}

func hasUnsafeSegment(value string) bool {
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.ContainsRune(segment, '\x00') || strings.ContainsRune(segment, '\\') {
			return true
		}
	}
	return false
}
