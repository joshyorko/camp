package s3store_test

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/joshyorko/camp/internal/adapters/s3store"
	"github.com/joshyorko/camp/internal/ports"
)

type memoryObject struct {
	body     string
	revision string
}

type memoryS3 struct {
	mu                 sync.Mutex
	objects            map[string]memoryObject
	nextRevision       int
	ignorePrecondition bool
}

func newMemoryS3() *memoryS3 {
	return &memoryS3{objects: make(map[string]memoryObject)}
}

func (s *memoryS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/bucket" && r.Method == http.MethodGet {
		s.list(w, r)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/bucket/") {
		http.NotFound(w, r)
		return
	}
	key, err := url.PathUnescape(strings.TrimPrefix(r.URL.EscapedPath(), "/bucket/"))
	if err != nil {
		http.Error(w, "bad key", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.objects[key]
	switch r.Method {
	case http.MethodPut:
		if !s.ignorePrecondition && (r.Header.Get("If-None-Match") == "*" && exists || r.Header.Get("If-Match") != "" && (!exists || r.Header.Get("If-Match") != current.revision)) {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			http.Error(w, readErr.Error(), http.StatusInternalServerError)
			return
		}
		s.nextRevision++
		revision := fmt.Sprintf("\"revision-%d\"", s.nextRevision)
		s.objects[key] = memoryObject{body: string(body), revision: revision}
		w.Header().Set("ETag", revision)
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
	case http.MethodHead:
		if !exists {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", current.revision)
		w.Header().Set("Content-Length", strconv.Itoa(len(current.body)))
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		if !exists {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", current.revision)
		w.Header().Set("Content-Length", strconv.Itoa(len(current.body)))
		_, _ = io.WriteString(w, current.body)
	case http.MethodDelete:
		if !s.ignorePrecondition && (r.Header.Get("If-Match") == "" || !exists || r.Header.Get("If-Match") != current.revision) {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		delete(s.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *memoryS3) list(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("list-type") != "2" {
		http.Error(w, "missing list-type", http.StatusBadRequest)
		return
	}
	prefix := r.URL.Query().Get("prefix")
	token := r.URL.Query().Get("continuation-token")
	type object struct {
		Key  string `xml:"Key"`
		Size int    `xml:"Size"`
		ETag string `xml:"ETag"`
	}
	type result struct {
		XMLName xml.Name `xml:"ListBucketResult"`
		Objects []object `xml:"Contents"`
		Next    string   `xml:"NextContinuationToken,omitempty"`
	}
	page := result{}
	if prefix == "root/" && token == "" {
		page.Objects = []object{{Key: "root/a", Size: 1, ETag: "\"a-r1\""}}
		page.Next = "opaque+page/token="
	} else if prefix == "root/" && token == "opaque+page/token=" {
		page.Objects = []object{{Key: "root/b", Size: 2, ETag: "\"b-r1\""}}
	} else {
		http.Error(w, "unexpected pagination query", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(page)
}

func newStore(t *testing.T, handler http.Handler) *s3store.Store {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	store, err := s3store.New(s3store.Config{
		Endpoint:   server.URL,
		Bucket:     "bucket",
		PathStyle:  true,
		HTTPClient: server.Client(),
		Signer: s3store.SignFunc(func(request *http.Request) error {
			request.Header.Set("Authorization", "signed-for-test")
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestVirtualHostAddressingPlacesBucketInHost(t *testing.T) {
	var host, requestPath string
	wantStop := errors.New("stop after address capture")
	store, err := s3store.New(s3store.Config{
		Endpoint: "https://s3.example.test:9000", Bucket: "camp-bucket",
		Signer: s3store.SignFunc(func(request *http.Request) error {
			host, requestPath = request.URL.Host, request.URL.Path
			return wantStop
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Head(context.Background(), "capsule/object"); !errors.Is(err, wantStop) {
		t.Fatalf("Head error = %v", err)
	}
	if host != "camp-bucket.s3.example.test:9000" || requestPath != "/capsule/object" {
		t.Fatalf("request address = %q %q", host, requestPath)
	}
}

func TestSigningFailureStopsRequest(t *testing.T) {
	want := errors.New("credential chain unavailable")
	storeWithFailure, err := s3store.New(s3store.Config{
		Endpoint: "https://s3.example.test", Bucket: "camp-bucket", PathStyle: true,
		Signer: s3store.SignFunc(func(*http.Request) error { return want }),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storeWithFailure.Head(context.Background(), "object"); !errors.Is(err, want) {
		t.Fatalf("Head error = %v, want signer failure", err)
	}
}

func TestConditionalMutationsUseOpaqueRevisions(t *testing.T) {
	server := newMemoryS3()
	store := newStore(t, server)
	ctx := context.Background()

	created, err := store.PutConditional(ctx, "capsule/latest.json", []byte("one"), ports.WriteCondition{MustBeAbsent: true})
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != `"revision-1"` {
		t.Fatalf("created revision = %q", created.Revision)
	}
	if _, err := store.PutConditional(ctx, "capsule/latest.json", []byte("duplicate"), ports.WriteCondition{MustBeAbsent: true}); !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("duplicate create error = %v, want conflict", err)
	}
	if _, err := store.PutConditional(ctx, "capsule/latest.json", []byte("stale"), ports.WriteCondition{MatchRevision: `"stale"`}); !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("stale replace error = %v, want conflict", err)
	}

	replaced, err := store.PutConditional(ctx, "capsule/latest.json", []byte("two"), ports.WriteCondition{MatchRevision: created.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Revision != `"revision-2"` {
		t.Fatalf("replaced revision = %q", replaced.Revision)
	}
	if err := store.DeleteConditional(ctx, "capsule/latest.json", created.Revision); !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("stale delete error = %v, want conflict", err)
	}
	if err := store.DeleteConditional(ctx, "capsule/latest.json", replaced.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Head(ctx, "capsule/latest.json"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("head after delete error = %v, want not found", err)
	}
}

func TestConditionalWriteRequiresExactlyOneCondition(t *testing.T) {
	store := newStore(t, newMemoryS3())
	for _, condition := range []ports.WriteCondition{
		{},
		{MustBeAbsent: true, MatchRevision: `"revision"`},
	} {
		if _, err := store.PutConditional(context.Background(), "key", []byte("body"), condition); !errors.Is(err, ports.ErrInvalidCondition) {
			t.Fatalf("condition %#v error = %v, want invalid condition", condition, err)
		}
	}
	if err := store.DeleteConditional(context.Background(), "key", ""); !errors.Is(err, ports.ErrInvalidCondition) {
		t.Fatalf("empty delete revision error = %v, want invalid condition", err)
	}
}

func TestGetHeadAndListPreserveS3MetadataAndOpaquePaginationTokens(t *testing.T) {
	server := newMemoryS3()
	server.objects["root/object name"] = memoryObject{body: "payload", revision: `"object-r1"`}
	store := newStore(t, server)

	reader, meta, err := store.Get(context.Background(), "root/object name")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if closeErr := reader.Close(); err == nil {
		err = closeErr
	}
	if err != nil || string(body) != "payload" || meta.Revision != `"object-r1"` || meta.Size != 7 {
		t.Fatalf("get = %q, %#v, %v", body, meta, err)
	}

	first, token, err := store.List(context.Background(), "root/", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Key != "root/a" || token != "opaque+page/token=" {
		t.Fatalf("first page = %#v, token %q", first, token)
	}
	second, token, err := store.List(context.Background(), "root/", token)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].Key != "root/b" || token != "" {
		t.Fatalf("second page = %#v, token %q", second, token)
	}
}

func TestProbeWriterAcceptsVerifiedConditionalEndpointAndCleansUp(t *testing.T) {
	server := newMemoryS3()
	store := newStore(t, server)
	if err := store.ProbeWriter(context.Background(), "camp-probes/"); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.objects) != 0 {
		t.Fatalf("probe left objects behind: %#v", server.objects)
	}
}

func TestProbeWriterReconcilesLostConditionalReplaceRequest(t *testing.T) {
	serverState := newMemoryS3()
	server := httptest.NewServer(serverState)
	t.Cleanup(server.Close)
	transport := &oneShotPutEOF{base: server.Client().Transport, failAt: 3}
	store, err := s3store.New(s3store.Config{
		Endpoint: server.URL, Bucket: "bucket", PathStyle: true, HTTPClient: &http.Client{Transport: transport},
		Signer: s3store.SignFunc(func(request *http.Request) error { return nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ProbeWriter(context.Background(), "camp-probes/"); err != nil {
		t.Fatal(err)
	}
	if !transport.failed {
		t.Fatal("transport did not lose the conditional replace request")
	}
}

type oneShotPutEOF struct {
	base   http.RoundTripper
	failAt int
	puts   int
	failed bool
}

func (t *oneShotPutEOF) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodPut {
		t.puts++
		if t.puts == t.failAt {
			t.failed = true
			return nil, io.ErrUnexpectedEOF
		}
	}
	return t.base.RoundTrip(request)
}

func TestProbeWriterFailsClosedWhenEndpointIgnoresConditions(t *testing.T) {
	server := newMemoryS3()
	server.ignorePrecondition = true
	store := newStore(t, server)
	if err := store.ProbeWriter(context.Background(), "camp-probes/"); !errors.Is(err, s3store.ErrUnsafeWriter) {
		t.Fatalf("probe error = %v, want unsafe writer", err)
	}
}
