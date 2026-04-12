package auth

import (
	"context"
	"net/http"
)

type HeadersOptions struct {
	UserAgent string
	GQL       bool
}

func (s *State) Headers(options HeadersOptions) http.Header {
	if s == nil {
		return make(http.Header)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.headersLocked(options)
}

func (s *State) HeadersProvider(options HeadersOptions) func(context.Context) (http.Header, error) {
	return func(ctx context.Context) (http.Header, error) {
		if err := s.Validate(ctx); err != nil {
			return nil, err
		}
		return s.Headers(options), nil
	}
}

func (s *State) headersLocked(options HeadersOptions) http.Header {
	headers := make(http.Header)
	headers.Set("Accept", "*/*")
	headers.Set("Accept-Language", "en-US")
	headers.Set("Pragma", "no-cache")
	headers.Set("Cache-Control", "no-cache")
	if s.clientInfo.ClientID != "" {
		headers.Set("Client-Id", s.clientInfo.ClientID)
	}
	if options.UserAgent != "" {
		headers.Set("User-Agent", options.UserAgent)
	}
	if s.sessionID != "" {
		headers.Set("Client-Session-Id", s.sessionID)
	}
	if s.deviceID != "" {
		headers.Set("X-Device-Id", s.deviceID)
	}
	if options.GQL {
		headers.Set("Origin", s.clientInfo.ClientURL)
		headers.Set("Referer", s.clientInfo.ClientURL)
		if s.accessToken != "" {
			headers.Set("Authorization", "OAuth "+s.accessToken)
		}
	}

	return headers
}
