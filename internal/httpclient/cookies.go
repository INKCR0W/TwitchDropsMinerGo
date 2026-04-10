package httpclient

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path"
	"sync"
	"time"

	"twitchdropsminergo/internal/storage"
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
	mu     sync.Mutex
	stored map[string]map[cookieKey]persistedCookie
}

func NewPersistentJar(path string) (*PersistentJar, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("创建 Cookie Jar 失败: %w", err)
	}

	persistentJar := &PersistentJar{
		path:   path,
		jar:    jar,
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

	j.jar.SetCookies(u, cookies)
	if u == nil {
		return
	}

	key := canonicalCookieURL(u)

	j.mu.Lock()
	defer j.mu.Unlock()

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

	return j.jar.Cookies(u)
}

func (j *PersistentJar) Load() error {
	if j == nil || j.path == "" {
		return nil
	}

	fileData, err := storage.LoadJSONFile(j.path, persistedJar{
		SchemaVersion: cookieSchemaVersion,
		Cookies:       map[string][]persistedCookie{},
	})
	if err != nil {
		return fmt.Errorf("读取 Cookie Jar 失败: %w", err)
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	j.stored = make(map[string]map[cookieKey]persistedCookie, len(fileData.Cookies))
	now := time.Now().UTC()
	for rawURL, cookies := range fileData.Cookies {
		parsedURL, parseErr := url.Parse(rawURL)
		if parseErr != nil {
			return fmt.Errorf("解析 Cookie 来源 URL %q 失败: %w", rawURL, parseErr)
		}

		httpCookies := make([]*http.Cookie, 0, len(cookies))
		for _, persisted := range cookies {
			cookie := &http.Cookie{
				Name:     persisted.Name,
				Value:    persisted.Value,
				Path:     persisted.Path,
				Domain:   persisted.Domain,
				Expires:  persisted.Expires,
				MaxAge:   persisted.MaxAge,
				Secure:   persisted.Secure,
				HttpOnly: persisted.HTTPOnly,
				SameSite: http.SameSite(persisted.SameSite),
			}
			if shouldDeleteCookie(cookie, now) {
				continue
			}

			httpCookies = append(httpCookies, cookie)
		}

		if len(httpCookies) == 0 {
			continue
		}

		perURL := make(map[cookieKey]persistedCookie, len(httpCookies))
		for _, cookie := range httpCookies {
			perURL[cookieKey{
				Name:   cookie.Name,
				Domain: cookie.Domain,
				Path:   cookie.Path,
			}] = persistedCookie{
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

		j.stored[canonicalCookieURL(parsedURL)] = perURL
		j.jar.SetCookies(parsedURL, httpCookies)
	}

	return nil
}

func (j *PersistentJar) Save() error {
	if j == nil || j.path == "" {
		return nil
	}

	j.mu.Lock()
	now := time.Now().UTC()
	fileData := persistedJar{
		SchemaVersion: cookieSchemaVersion,
		Cookies:       make(map[string][]persistedCookie, len(j.stored)),
	}
	for rawURL, cookiesByKey := range j.stored {
		cookies := make([]persistedCookie, 0, len(cookiesByKey))
		for _, persisted := range cookiesByKey {
			cookie := &http.Cookie{
				Name:     persisted.Name,
				Value:    persisted.Value,
				Path:     persisted.Path,
				Domain:   persisted.Domain,
				Expires:  persisted.Expires,
				MaxAge:   persisted.MaxAge,
				Secure:   persisted.Secure,
				HttpOnly: persisted.HTTPOnly,
				SameSite: http.SameSite(persisted.SameSite),
			}
			if shouldDeleteCookie(cookie, now) {
				continue
			}

			cookies = append(cookies, persisted)
		}

		if len(cookies) == 0 {
			continue
		}

		fileData.Cookies[rawURL] = cookies
	}
	j.mu.Unlock()

	return storage.SaveJSONFile(j.path, fileData)
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

func cookiePathOrDefault(value string) string {
	if value == "" {
		return "/"
	}

	cleaned := path.Clean(value)
	if cleaned == "." {
		return "/"
	}
	if cleaned[0] != '/' {
		return "/" + cleaned
	}

	return cleaned
}
