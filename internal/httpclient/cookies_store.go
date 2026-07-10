package httpclient

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"

	"twitchdropsminergo/internal/secure"
	"twitchdropsminergo/internal/storage"
)

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

func newEmptyPersistedJar() persistedJar {
	return persistedJar{
		SchemaVersion: cookieSchemaVersion,
		Cookies:       map[string][]persistedCookie{},
	}
}
