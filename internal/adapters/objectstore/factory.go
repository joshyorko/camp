package objectstore

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/joshyorko/camp/internal/adapters/filebackend"
	"github.com/joshyorko/camp/internal/adapters/s3store"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/ports"
)

type Options struct {
	HTTPClient *http.Client
	Signer     s3store.Signer
}

const writerProbePrefix = "camp-probes/"

func NewWriter(ctx context.Context, backend config.Backend, options Options) (ports.ObjectStore, error) {
	store, err := New(ctx, backend, options)
	if err != nil {
		return nil, err
	}
	if backend.Kind != config.BackendS3 {
		return store, nil
	}
	prober, ok := store.(interface {
		ProbeWriter(context.Context, string) error
	})
	if !ok {
		return nil, errors.New("S3 object store does not support writer safety probing")
	}
	if err := prober.ProbeWriter(ctx, writerProbePrefix); err != nil {
		return nil, fmt.Errorf("verify S3 writer safety: %w", err)
	}
	return store, nil
}

func New(ctx context.Context, backend config.Backend, options Options) (ports.ObjectStore, error) {
	switch backend.Kind {
	case config.BackendFile:
		if backend.File == nil {
			return nil, errors.New("file backend descriptor is incomplete")
		}
		return filebackend.New(backend.File.Root)
	case config.BackendS3:
		if backend.S3 == nil {
			return nil, errors.New("S3 backend descriptor is incomplete")
		}
		signer := options.Signer
		if signer == nil {
			awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(backend.S3.Region))
			if err != nil {
				return nil, fmt.Errorf("load host AWS credential chain: %w", err)
			}
			signer = &credentialChainSigner{region: backend.S3.Region, credentials: awsConfig.Credentials, signer: v4.NewSigner()}
		}
		return s3store.New(s3store.Config{
			Endpoint: backend.S3.Endpoint, Bucket: backend.S3.Bucket, Prefix: backend.S3.Prefix,
			PathStyle: backend.S3.PathStyle, HTTPClient: options.HTTPClient, Signer: signer,
		})
	default:
		return nil, fmt.Errorf("unsupported backend kind %q", backend.Kind)
	}
}

type credentialChainSigner struct {
	region      string
	credentials aws.CredentialsProvider
	signer      *v4.Signer
}

func (s *credentialChainSigner) Sign(request *http.Request) error {
	credentials, err := s.credentials.Retrieve(request.Context())
	if err != nil {
		return fmt.Errorf("retrieve host AWS credentials: %w", err)
	}
	request.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
	return s.signer.SignHTTP(request.Context(), credentials, request, "UNSIGNED-PAYLOAD", "s3", s.region, time.Now().UTC())
}
