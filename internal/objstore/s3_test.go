package objstore

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestS3Client_ListObjectsReportsCancellation(t *testing.T) {
	srv := httptest.NewServer(&fakeListServer{objects: []ObjectInfo{{Key: "a", Size: 1}}})
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := newTestS3Client(t, srv).ListObjects(ctx, "conquest", "", func(ObjectInfo) bool { return true })
	if err == nil {
		t.Fatal("expected an error for a cancelled listing")
	}
}

func TestS3Client_ListObjectsStoppedEarlyLeaksNoGoroutines(t *testing.T) {
	fake := &fakeListServer{objects: []ObjectInfo{
		{Key: "a", Size: 1}, {Key: "b", Size: 1}, {Key: "c", Size: 1}, {Key: "d", Size: 1},
	}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	client := newTestS3Client(t, srv)

	// Warm up the HTTP client's connection goroutines.
	_ = client.ListObjects(context.Background(), "conquest", "", func(ObjectInfo) bool { return false })
	before := runtime.NumGoroutine()
	for range 20 {
		if err := client.ListObjects(context.Background(), "conquest", "", func(ObjectInfo) bool { return false }); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Fatalf("goroutines: %d before 20 stopped listings, %d after", before, after)
	}
}

// fakeListServer answers S3 ListObjectsV2 requests with pages of
// objects, two per page, and records the requests it got.
type fakeListServer struct {
	mu       sync.Mutex
	requests int
	objects  []ObjectInfo
	fail     bool
}

func (s *fakeListServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests++
	s.mu.Unlock()
	if s.fail {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`)
		return
	}
	if r.URL.Query().Get("list-type") != "2" {
		http.Error(w, "unexpected request "+r.URL.String(), http.StatusBadRequest)
		return
	}
	prefix := r.URL.Query().Get("prefix")
	var matching []ObjectInfo
	for _, obj := range s.objects {
		if strings.HasPrefix(obj.Key, prefix) {
			matching = append(matching, obj)
		}
	}
	start := 0
	if token := r.URL.Query().Get("continuation-token"); token != "" {
		fmt.Sscanf(token, "%d", &start)
	}
	end := min(start+2, len(matching))
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	fmt.Fprintf(&b, "<Name>conquest</Name><Prefix>%s</Prefix><KeyCount>%d</KeyCount><MaxKeys>1000</MaxKeys>", prefix, end-start)
	for _, obj := range matching[start:end] {
		fmt.Fprintf(&b, "<Contents><Key>%s</Key><LastModified>2026-11-14T00:00:00.000Z</LastModified><ETag>&quot;x&quot;</ETag><Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>", obj.Key, obj.Size)
	}
	if end < len(matching) {
		fmt.Fprintf(&b, "<IsTruncated>true</IsTruncated><NextContinuationToken>%d</NextContinuationToken>", end)
	} else {
		b.WriteString("<IsTruncated>false</IsTruncated>")
	}
	b.WriteString("</ListBucketResult>")
	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprint(w, b.String())
}

func newTestS3Client(t *testing.T, srv *httptest.Server) *S3Client {
	t.Helper()
	client, err := NewS3Client(S3Config{
		Endpoint:  strings.TrimPrefix(srv.URL, "http://"),
		AccessKey: "test",
		SecretKey: "test",
		Region:    "us-east-1",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

func TestS3Client_ListObjectsReportsKeysAndSizes(t *testing.T) {
	fake := &fakeListServer{objects: []ObjectInfo{
		{Key: "runs/1/outputs/a.txt", Size: 1},
		{Key: "runs/1/outputs/b/c.txt", Size: 22},
		{Key: "runs/1/outputs/d.txt", Size: 333},
		{Key: "runs/2/outputs/e.txt", Size: 4},
	}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	var got []ObjectInfo
	err := newTestS3Client(t, srv).ListObjects(context.Background(), "conquest", "runs/1/", func(obj ObjectInfo) bool {
		got = append(got, obj)
		return true
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := fake.objects[:3]
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("objects = %v, want %v", got, want)
	}
}

func TestS3Client_ListObjectsStopsWhenCallbackReturnsFalse(t *testing.T) {
	fake := &fakeListServer{objects: []ObjectInfo{
		{Key: "a", Size: 1}, {Key: "b", Size: 1}, {Key: "c", Size: 1},
		{Key: "d", Size: 1}, {Key: "e", Size: 1}, {Key: "f", Size: 1},
	}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	var got []string
	err := newTestS3Client(t, srv).ListObjects(context.Background(), "conquest", "", func(obj ObjectInfo) bool {
		got = append(got, obj.Key)
		return len(got) < 2
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Join(got, ",") != "a,b" {
		t.Fatalf("keys = %v, want a,b", got)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	// The first page holds a and b; the client may prefetch the next page
	// before it sees the cancellation, but never the third.
	if fake.requests > 2 {
		t.Fatalf("server got %d list requests, want at most 2", fake.requests)
	}
}

func TestS3Client_ListObjectsReturnsErrors(t *testing.T) {
	srv := httptest.NewServer(&fakeListServer{fail: true})
	defer srv.Close()

	err := newTestS3Client(t, srv).ListObjects(context.Background(), "conquest", "runs/", func(ObjectInfo) bool {
		t.Fatal("callback called for a failed listing")
		return true
	})
	if err == nil {
		t.Fatal("expected an error")
	}
}
