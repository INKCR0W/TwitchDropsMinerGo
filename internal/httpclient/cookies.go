package httpclient

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

const cookieSchemaVersion = 1

type cookieKey struct {
	Name   string
	Domain string
	Path   string
}

type persistedJar struct {
	SchemaVersion int                          `json:"schema_version"`
	Cookies       map[string][]persistedCookie `json:"cookies"`
}

type persistedCookie struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Path     string    `json:"path,omitempty"`
	Domain   string    `json:"domain,omitempty"`
	Expires  time.Time `json:"expires,omitempty"`
	MaxAge   int       `json:"max_age,omitempty"`
	Secure   bool      `json:"secure,omitempty"`
	HTTPOnly bool      `json:"http_only,omitempty"`
	SameSite int       `json:"same_site,omitempty"`
}

type PersistentJar struct {
	path   string
	jar    *cookiejar.Jar
	logger *slog.Logger
	mu     sync.Mutex
	saveMu sync.Mutex
	stored map[string]map[cookieKey]persistedCookie
}

func NewPersistentJar(path string, logger *slog.Logger) (*PersistentJar, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("创建 Cookie Jar 失败: %w", err)
	}

	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	persistentJar := &PersistentJar{
		path:   path,
		jar:    jar,
		logger: logger,
		stored: make(map[string]map[cookieKey]persistedCookie),
	}

	if err := persistentJar.Load(); err != nil {
		return nil, err
	}

	return persistentJar, nil
}

func (j *PersistentJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	if j == nil {
		return
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	j.jar.SetCookies(u, cookies)
	if u == nil {
		return
	}

	key := canonicalCookieURL(u)

	perURL := j.stored[key]
	if perURL == nil {
		perURL = make(map[cookieKey]persistedCookie)
		j.stored[key] = perURL
	}

	for _, cookie := range cookies {
		if cookie == nil || cookie.Name == "" {
			continue
		}

		cookieKey := cookieKey{
			Name:   cookie.Name,
			Domain: cookie.Domain,
			Path:   cookie.Path,
		}

		if shouldDeleteCookie(cookie, time.Now().UTC()) {
			delete(perURL, cookieKey)
			continue
		}

		perURL[cookieKey] = persistedCookie{
			Name:     cookie.Name,
			Value:    cookie.Value,
			Path:     cookie.Path,
			Domain:   cookie.Domain,
			Expires:  cookie.Expires,
			MaxAge:   cookie.MaxAge,
			Secure:   cookie.Secure,
			HTTPOnly: cookie.HttpOnly,
			SameSite: int(cookie.SameSite),
		}
	}

	if len(perURL) == 0 {
		delete(j.stored, key)
	}
}

func (j *PersistentJar) Cookies(u *url.URL) []*http.Cookie {
	if j == nil {
		return nil
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	return j.jar.Cookies(u)
}

func (j *PersistentJar) Clear() error {
	if j == nil {
		return nil
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	j.stored = make(map[string]map[cookieKey]persistedCookie)
	return j.rebuildLocked()
}

func (j *PersistentJar) ClearDomain(domain string) error {
	if j == nil {
		return nil
	}

	domain = canonicalCookieDomain(domain)
	if domain == "" {
		return nil
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	for rawURL, cookiesByKey := range j.stored {
		rawHost := ""
		if parsedURL, err := url.Parse(rawURL); err == nil {
			rawHost = canonicalCookieDomain(parsedURL.Hostname())
		}
		for key := range cookiesByKey {
			if domainMatches(canonicalCookieDomain(key.Domain), domain) || domainMatches(rawHost, domain) {
				delete(cookiesByKey, key)
			}
		}
		if len(cookiesByKey) == 0 {
			delete(j.stored, rawURL)
		}
	}

	return j.rebuildLocked()
}

func canonicalCookieURL(u *url.URL) string {
	if u == nil {
		return ""
	}

	normalized := &url.URL{
		Scheme: u.Scheme,
		Host:   u.Host,
		Path:   u.EscapedPath(),
	}
	if normalized.Path == "" {
		normalized.Path = "/"
	}

	return normalized.String()
}

func shouldDeleteCookie(cookie *http.Cookie, now time.Time) bool {
	if cookie == nil {
		return true
	}
	if cookie.MaxAge < 0 {
		return true
	}
	if !cookie.Expires.IsZero() && !cookie.Expires.After(now) {
		return true
	}
	return false
}

func canonicalCookieDomain(domain string) string {
	domain = strings.TrimSpace(domain)
	domain = strings.TrimPrefix(domain, ".")
	return strings.ToLower(domain)
}

func domainMatches(host string, domain string) bool {
	if host == "" || domain == "" {
		return false
	}
	if host == domain {
		return true
	}

	return strings.HasSuffix(host, "."+domain)
}
