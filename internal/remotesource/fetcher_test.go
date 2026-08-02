package remotesource

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFetchPlaintextPreservesOrderDuplicatesAndContent(t *testing.T) {
	want := []string{
		" vless://first ",
		"unsupported://kept",
		" vless://first ",
	}
	server := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, " vless://first \r\n\r\n \t\r\nunsupported://kept\n vless://first \n")
	}))

	result := fetchFrom(t, server.URL)
	if result.Encoding != EncodingPlain {
		t.Fatalf("encoding = %q, want %q", result.Encoding, EncodingPlain)
	}
	if !reflect.DeepEqual(result.Lines, want) {
		t.Fatalf("lines = %#v, want %#v", result.Lines, want)
	}
	if result.FetchedAt.IsZero() {
		t.Fatal("FetchedAt is zero")
	}
}

func TestFetchDetectsStandardAndRawBase64(t *testing.T) {
	decoded := "vless://one\nss://two\nvless://one\n"
	tests := map[string]string{
		"standard": base64.StdEncoding.EncodeToString([]byte(decoded)),
		"raw":      base64.RawStdEncoding.EncodeToString([]byte(decoded)),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			server := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, body)
			}))
			result := fetchFrom(t, server.URL)
			if result.Encoding != EncodingBase64 {
				t.Fatalf("encoding = %q, want %q", result.Encoding, EncodingBase64)
			}
			want := []string{"vless://one", "ss://two", "vless://one"}
			if !reflect.DeepEqual(result.Lines, want) {
				t.Fatalf("lines = %#v, want %#v", result.Lines, want)
			}
		})
	}
}

func TestFetchFallsBackToPlaintextWhenBase64DecodeIsNotValidLineData(t *testing.T) {
	tests := map[string]string{
		"decoded invalid UTF-8": "/w==",
		"decoded empty":         "IA==",
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			server := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, body)
			}))
			result := fetchFrom(t, server.URL)
			if result.Encoding != EncodingPlain || !reflect.DeepEqual(result.Lines, []string{body}) {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestFetchRejectsInvalidUTF8Plaintext(t *testing.T) {
	server := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte{0xff, 0xfe})
	}))
	fetcher, err := New()
	if err != nil {
		t.Fatal(err)
	}
	_, err = fetcher.Fetch(context.Background(), server.URL)
	if !errors.Is(err, ErrInvalidUTF8) {
		t.Fatalf("err = %v, want ErrInvalidUTF8", err)
	}
}

func TestFetchEnforcesByteLimitForStreamingResponse(t *testing.T) {
	server := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		fmt.Fprint(w, "123456")
	}))
	fetcher, err := New(WithMaxBytes(5))
	if err != nil {
		t.Fatal(err)
	}
	_, err = fetcher.Fetch(context.Background(), server.URL)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
}

func TestFetchEnforcesNonEmptyLineLimitAfterDecode(t *testing.T) {
	body := base64.StdEncoding.EncodeToString([]byte("one\n\ntwo\nthree\n"))
	server := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, body)
	}))
	fetcher, err := New(WithMaxLines(2))
	if err != nil {
		t.Fatal(err)
	}
	_, err = fetcher.Fetch(context.Background(), server.URL)
	if !errors.Is(err, ErrTooManyLines) {
		t.Fatalf("err = %v, want ErrTooManyLines", err)
	}
}

func TestFetchAllowsPrivateAddressAndRedirectsWithinLimit(t *testing.T) {
	server := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/middle", http.StatusFound)
		case "/middle":
			http.Redirect(w, r, "/final", http.StatusTemporaryRedirect)
		default:
			fmt.Fprint(w, "vless://private")
		}
	}))
	fetcher, err := New(WithMaxRedirects(2))
	if err != nil {
		t.Fatal(err)
	}
	result, err := fetcher.Fetch(context.Background(), server.URL+"/start")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Lines, []string{"vless://private"}) {
		t.Fatalf("lines = %#v", result.Lines)
	}
}

func TestFetchRejectsRedirectOverLimit(t *testing.T) {
	server := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := 0
		_, _ = fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/"), "%d", &n)
		http.Redirect(w, r, fmt.Sprintf("/%d", n+1), http.StatusFound)
	}))
	fetcher, err := New(WithMaxRedirects(2))
	if err != nil {
		t.Fatal(err)
	}
	_, err = fetcher.Fetch(context.Background(), server.URL+"/0")
	if !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("err = %v, want ErrTooManyRedirects", err)
	}
}

func TestFetchRejectsNonHTTPURLAndRedirect(t *testing.T) {
	fetcher, err := New()
	if err != nil {
		t.Fatal(err)
	}
	_, err = fetcher.Fetch(context.Background(), "file:///tmp/source")
	if !errors.Is(err, ErrUnsupportedScheme) {
		t.Fatalf("err = %v, want ErrUnsupportedScheme", err)
	}

	server := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "ftp://example.com/source")
		w.WriteHeader(http.StatusFound)
	}))
	_, err = fetcher.Fetch(context.Background(), server.URL)
	if !errors.Is(err, ErrUnsupportedScheme) {
		t.Fatalf("redirect err = %v, want ErrUnsupportedScheme", err)
	}
}

func TestFetchEnforcesTimeout(t *testing.T) {
	server := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		fmt.Fprint(w, "late")
	}))
	fetcher, err := New(WithTimeout(10 * time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	_, err = fetcher.Fetch(context.Background(), server.URL)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context deadline exceeded", err)
	}
}

func TestFetchReturnsHTTPStatusError(t *testing.T) {
	server := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	fetcher, err := New()
	if err != nil {
		t.Fatal(err)
	}
	_, err = fetcher.Fetch(context.Background(), server.URL)
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("err = %#v, want HTTPStatusError 503", err)
	}
}

func TestOptionsValidateLimits(t *testing.T) {
	tests := []Option{
		WithTimeout(0),
		WithMaxBytes(0),
		WithMaxRedirects(-1),
		WithMaxLines(0),
		WithHTTPClient(nil),
		nil,
	}
	for _, option := range tests {
		if _, err := New(option); err == nil {
			t.Fatalf("New(%v) succeeded, want error", option)
		}
	}
}

func fetchFrom(t *testing.T, sourceURL string) Result {
	t.Helper()
	fetcher, err := New()
	if err != nil {
		t.Fatal(err)
	}
	result, err := fetcher.Fetch(context.Background(), sourceURL)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func newServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}
