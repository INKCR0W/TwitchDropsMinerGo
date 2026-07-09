package httpclient

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"twitchdropsminergo/internal/secure"
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
	logger *slog.Logger
	mu     sync.Mutex
	saveMu sync.Mutex
	stored map[string]map[cookieKey]persistedCookie
}

func newEmptyPersistedJar() persistedJar {
	return persistedJar{
		SchemaVersion: cookieSchemaVersion,
		Cookies:       map[string][]persistedCookie{},
	}
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

func (j *PersistentJar) Load() error {
	if j == nil || j.path == "" {
		return nil
	}

	fileData, err := storage.LoadJSONFile(j.path, newEmptyPersistedJar())
	switch {
	case errors.Is(err, storage.ErrCorrupt):
		// 损坏即丢 auth-token,以空 jar 继续交给 device code 流程重新登录;
		// 隔离只是尽力保留现场,失败也不能让 24/7 服务退出
		quarantined, quarantineErr := storage.QuarantineCorrupt(j.path)
		if quarantineErr != nil {
			j.logger.Warn(
				"隔离损坏的 Cookie 文件失败，仍将以空 jar 重新登录",
				"path", j.path,
				"quarantined", quarantined,
				"error", quarantineErr,
				"cause", err,
			)
		} else {
			j.logger.Warn(
				"Cookie 文件已损坏，已隔离并将重新登录",
				"path", j.path,
				"quarantined", quarantined,
				"error", err,
			)
		}
		// LoadJSONFile 会把 defaults 的 map 直接喂给 json.Unmarshal,类型不匹配时
		// 它已被部分填充,不能复用
		fileData = newEmptyPersistedJar()
	case err != nil:
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
	}

	return j.rebuildLocked()
}

func (j *PersistentJar) Save() error {
	if j == nil || j.path == "" {
		return nil
	}

	j.saveMu.Lock()
	defer j.saveMu.Unlock()

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

	if err := storage.SaveJSONFile(j.path, fileData); err != nil {
		return err
	}
	_ = secure.HardenFile(j.path)
	return nil
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

func (j *PersistentJar) rebuildLocked() error {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return fmt.Errorf("重建 Cookie Jar 失败: %w", err)
	}

	for rawURL, cookiesByKey := range j.stored {
		if len(cookiesByKey) == 0 {
			continue
		}

		parsedURL, parseErr := url.Parse(rawURL)
		if parseErr != nil {
			return fmt.Errorf("解析 Cookie 来源 URL %q 失败: %w", rawURL, parseErr)
		}

		httpCookies := make([]*http.Cookie, 0, len(cookiesByKey))
		for _, persisted := range cookiesByKey {
			httpCookies = append(httpCookies, &http.Cookie{
				Name:     persisted.Name,
				Value:    persisted.Value,
				Path:     persisted.Path,
				Domain:   persisted.Domain,
				Expires:  persisted.Expires,
				MaxAge:   persisted.MaxAge,
				Secure:   persisted.Secure,
				HttpOnly: persisted.HTTPOnly,
				SameSite: http.SameSite(persisted.SameSite),
			})
		}

		jar.SetCookies(parsedURL, httpCookies)
	}

	j.jar = jar
	return nil
}
