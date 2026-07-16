package ports

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrNotFound         = errors.New("object not found")
	ErrConflict         = errors.New("object revision conflict")
	ErrIntegrity        = errors.New("object integrity check failed")
	ErrInvalidKey       = errors.New("invalid object key")
	ErrUnsafePath       = errors.New("unsafe object path")
	ErrInvalidCondition = errors.New("invalid write condition")
	ErrInvalidPageToken = errors.New("invalid page token")
	ErrAmbiguous        = errors.New("object mutation outcome is ambiguous")
)

type Revision string

type ObjectMeta struct {
	Key      string
	Size     int64
	Revision Revision
	SHA256   string
	Modified time.Time
}

type WriteCondition struct {
	MustBeAbsent  bool
	MatchRevision Revision
}

type RestartableSource interface {
	Open() (io.ReadCloser, error)
}

type ObjectStore interface {
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectMeta, error)
	Head(ctx context.Context, key string) (ObjectMeta, error)
	PutImmutable(ctx context.Context, key string, source RestartableSource, expectedSHA256 string, expectedSize int64) (ObjectMeta, error)
	PutConditional(ctx context.Context, key string, body []byte, condition WriteCondition) (ObjectMeta, error)
	DeleteConditional(ctx context.Context, key string, expected Revision) error
	List(ctx context.Context, prefix, pageToken string) (items []ObjectMeta, nextPageToken string, err error)
}
