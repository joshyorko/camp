package ports

import (
	"context"
	"io"
	"time"
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

type ArtifactStore interface {
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectMeta, error)
	Head(ctx context.Context, key string) (ObjectMeta, error)
	PutImmutable(ctx context.Context, key string, source RestartableSource, expectedSHA256 string, expectedSize int64) (ObjectMeta, error)
	PutConditional(ctx context.Context, key string, body []byte, condition WriteCondition) (ObjectMeta, error)
	DeleteConditional(ctx context.Context, key string, expected Revision) error
	List(ctx context.Context, prefix, pageToken string) (items []ObjectMeta, nextPageToken string, err error)
}
