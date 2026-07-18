package objectstore_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/joshyorko/camp/internal/adapters/objectstore"
	"github.com/joshyorko/camp/internal/adapters/s3store"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/ports"
)

func TestFactoryRetainsFileBackendBehavior(t *testing.T) {
	root := filepath.Join(t.TempDir(), "objects")
	backend, err := config.ResolveBackend("file://"+root, config.S3Values{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := objectstore.New(context.Background(), backend, objectstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutConditional(context.Background(), "pointer", []byte("one"), ports.WriteCondition{MustBeAbsent: true}); err != nil {
		t.Fatal(err)
	}
}

func TestFactoryBuildsS3StoreWithInjectedHostSigner(t *testing.T) {
	signed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "host-credential" {
			t.Error("request was not signed")
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	backend, err := config.ResolveBackend("s3://bucket/prefix", config.S3Values{Endpoint: server.URL, Region: "us-east-1", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	store, err := objectstore.New(context.Background(), backend, objectstore.Options{
		HTTPClient: server.Client(), Signer: s3store.SignFunc(func(request *http.Request) error {
			signed = true
			request.Header.Set("Authorization", "host-credential")
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Head(context.Background(), "object"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Head error = %v", err)
	}
	if !signed {
		t.Fatal("host signer was not used")
	}
}
