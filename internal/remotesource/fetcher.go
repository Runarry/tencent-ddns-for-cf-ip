// Package remotesource fetches remote, line-oriented subscription sources.
package remotesource

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultTimeout      = 10 * time.Second
	DefaultMaxBytes     = int64(2 * 1024 * 1024)
	DefaultMaxRedirects = 5
	DefaultMaxLines     = 10_000

	EncodingPlain  = "plain"
	EncodingBase64 = "base64"
)

var (
	ErrUnsupportedScheme = errors.New("remote source URL must use http or https")
	ErrResponseTooLarge  = errors.New("remote source response exceeds byte limit")
	ErrTooManyRedirects  = errors.New("remote source exceeds redirect limit")
	ErrTooManyLines      = errors.New("remote source exceeds line limit")
	ErrInvalidUTF8       = errors.New("remote source is not valid UTF-8")
)

// Result is the normalized content of a successfully fetched source. Lines
// retain their source order and duplicates. Encoding is either EncodingPlain
// or EncodingBase64.
type Result struct {
	Lines     []string  `json:"lines"`
	Encoding  string    `json:"encoding"`
	FetchedAt time.Time `json:"fetched_at"`
}

// HTTPStatusError reports a non-2xx response from a remote source.
type HTTPStatusError struct {
	StatusCode int
	Status     string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("remote source returned HTTP status %s", e.Status)
}

// Option configures a Fetcher.
type Option func(*Fetcher) error

// Fetcher fetches and decodes remote subscription sources.
type Fetcher struct {
	timeout      time.Duration
	maxBytes     int64
	maxRedirects int
	maxLines     int
	client       *http.Client
	now          func() time.Time
}

// New returns a Fetcher with a 10 second timeout, a 2 MiB response limit, at
// most 5 redirects, and at most 10,000 non-empty lines.
func New(options ...Option) (*Fetcher, error) {
	f := &Fetcher{
		timeout:      DefaultTimeout,
		maxBytes:     DefaultMaxBytes,
		maxRedirects: DefaultMaxRedirects,
		maxLines:     DefaultMaxLines,
		client:       http.DefaultClient,
		now:          time.Now,
	}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("nil remote source option")
		}
		if err := option(f); err != nil {
			return nil, err
		}
	}
	return f, nil
}

func WithTimeout(timeout time.Duration) Option {
	return func(f *Fetcher) error {
		if timeout <= 0 {
			return errors.New("remote source timeout must be positive")
		}
		f.timeout = timeout
		return nil
	}
}

func WithMaxBytes(maxBytes int64) Option {
	return func(f *Fetcher) error {
		if maxBytes <= 0 || maxBytes == math.MaxInt64 {
			return errors.New("remote source byte limit must be positive and less than math.MaxInt64")
		}
		f.maxBytes = maxBytes
		return nil
	}
}

func WithMaxRedirects(maxRedirects int) Option {
	return func(f *Fetcher) error {
		if maxRedirects < 0 {
			return errors.New("remote source redirect limit cannot be negative")
		}
		f.maxRedirects = maxRedirects
		return nil
	}
}

func WithMaxLines(maxLines int) Option {
	return func(f *Fetcher) error {
		if maxLines <= 0 {
			return errors.New("remote source line limit must be positive")
		}
		f.maxLines = maxLines
		return nil
	}
}

// WithHTTPClient supplies transport-level client behavior such as a custom
// Transport or cookie jar. Fetcher timeout and redirect policy still apply.
func WithHTTPClient(client *http.Client) Option {
	return func(f *Fetcher) error {
		if client == nil {
			return errors.New("remote source HTTP client cannot be nil")
		}
		f.client = client
		return nil
	}
}

// Fetch retrieves sourceURL, detects standard or raw standard Base64 when its
// decoded value is non-empty UTF-8 line data, and otherwise reads the response
// as UTF-8 plaintext.
func (f *Fetcher) Fetch(ctx context.Context, sourceURL string) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("remote source context cannot be nil")
	}
	if err := validateURL(sourceURL); err != nil {
		return Result{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return Result{}, fmt.Errorf("create remote source request: %w", err)
	}

	client := *f.client
	client.Timeout = 0
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := validateParsedURL(req.URL); err != nil {
			return err
		}
		if len(via) > f.maxRedirects {
			return ErrTooManyRedirects
		}
		return nil
	}

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("fetch remote source: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return Result{}, &HTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status}
	}
	if resp.ContentLength > f.maxBytes {
		return Result{}, ErrResponseTooLarge
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes+1))
	if err != nil {
		return Result{}, fmt.Errorf("read remote source: %w", err)
	}
	if int64(len(body)) > f.maxBytes {
		return Result{}, ErrResponseTooLarge
	}

	lines, encoding, err := decodeLines(body, f.maxLines)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Lines:     lines,
		Encoding:  encoding,
		FetchedAt: f.now().UTC(),
	}, nil
}

func validateURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse remote source URL: %w", err)
	}
	return validateParsedURL(parsed)
}

func validateParsedURL(parsed *url.URL) error {
	if parsed == nil || (strings.ToLower(parsed.Scheme) != "http" && strings.ToLower(parsed.Scheme) != "https") {
		return ErrUnsupportedScheme
	}
	if parsed.Host == "" {
		return errors.New("remote source URL must include a host")
	}
	return nil
}

func decodeLines(body []byte, maxLines int) ([]string, string, error) {
	trimmed := strings.TrimSpace(string(body))
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		decoded, err := encoding.DecodeString(trimmed)
		if err != nil || !utf8.Valid(decoded) {
			continue
		}
		lines, err := nonEmptyLines(string(decoded), maxLines)
		if err != nil {
			return nil, "", err
		}
		if len(lines) > 0 {
			return lines, EncodingBase64, nil
		}
	}

	if !utf8.Valid(body) {
		return nil, "", ErrInvalidUTF8
	}
	lines, err := nonEmptyLines(string(body), maxLines)
	if err != nil {
		return nil, "", err
	}
	return lines, EncodingPlain, nil
}

func nonEmptyLines(value string, maxLines int) ([]string, error) {
	parts := strings.Split(value, "\n")
	lines := make([]string, 0, min(len(parts), maxLines))
	for _, line := range parts {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(lines) == maxLines {
			return nil, ErrTooManyLines
		}
		lines = append(lines, line)
	}
	return lines, nil
}
