package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/joshyorko/camp/internal/ports"
)

func TestCatalogPaginatesRepositoriesAndTagsAndResolvesDigests(t *testing.T) {
	t.Parallel()
	manifestBodies := map[string][]byte{
		"/v2/alpha/manifests/v1":           []byte(`{"schemaVersion":2,"tag":"v1"}`),
		"/v2/alpha/manifests/v2":           []byte(`{"schemaVersion":2,"tag":"v2"}`),
		"/v2/nested/team/manifests/latest": []byte(`{"schemaVersion":2,"tag":"latest"}`),
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if body, ok := manifestBodies[request.URL.Path]; ok && request.Method == http.MethodGet {
			response.Header().Set("Docker-Content-Digest", digestBody(body))
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write(body)
			return
		}
		switch request.URL.Path {
		case "/v2/_catalog":
			if request.URL.Query().Get("last") == "" {
				response.Header().Set("Link", `</v2/_catalog?n=2&last=zeta>; rel="next"`)
				_ = json.NewEncoder(response).Encode(map[string]any{"repositories": []string{"zeta", "alpha"}})
			} else {
				_ = json.NewEncoder(response).Encode(map[string]any{"repositories": []string{"nested/team"}})
			}
		case "/v2/alpha/tags/list":
			if request.URL.Query().Get("last") == "" {
				response.Header().Set("Link", `</v2/alpha/tags/list?n=2&last=v2>; rel="next"`)
				_ = json.NewEncoder(response).Encode(map[string]any{"name": "alpha", "tags": []string{"v2"}})
			} else {
				_ = json.NewEncoder(response).Encode(map[string]any{"name": "alpha", "tags": []string{"v1"}})
			}
		case "/v2/nested/team/tags/list":
			_ = json.NewEncoder(response).Encode(map[string]any{"name": "nested/team", "tags": []string{"latest"}})
		case "/v2/zeta/tags/list":
			_ = json.NewEncoder(response).Encode(map[string]any{"name": "zeta", "tags": nil})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	catalog := NewCatalog(server.Client(), 2)
	got, err := catalog.List(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	want := []ports.RegistryReference{
		{Repository: "alpha", Tag: "v1", ManifestDigest: digestBody(manifestBodies["/v2/alpha/manifests/v1"])},
		{Repository: "alpha", Tag: "v2", ManifestDigest: digestBody(manifestBodies["/v2/alpha/manifests/v2"])},
		{Repository: "nested/team", Tag: "latest", ManifestDigest: digestBody(manifestBodies["/v2/nested/team/manifests/latest"])},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("references = %#v, want %#v", got, want)
	}
}

func digestBody(body []byte) string {
	digest := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func TestCatalogRejectsDigestResolutionMismatch(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Docker-Content-Digest", "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	catalog := NewCatalog(server.Client(), 100)
	_, err := catalog.Resolve(context.Background(), server.URL, "team/app", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if !errors.Is(err, ErrRegistryDigestMismatch) {
		t.Fatalf("Resolve() error = %v, want ErrRegistryDigestMismatch", err)
	}
}

func TestCatalogMapsMissingManifestToObjectNotFound(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	_, err := NewCatalog(server.Client(), 100).Resolve(context.Background(), server.URL, "team/app", "v1")
	if !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Resolve() error = %v, want ports.ErrNotFound", err)
	}
}
