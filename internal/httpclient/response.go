package httpclient

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func (r Response) DecodeJSON(target any) error {
	if err := json.Unmarshal(r.Body, target); err != nil {
		return fmt.Errorf("解析响应 JSON 失败: %w", err)
	}

	return nil
}

func (r Response) Text() string {
	return string(r.Body)
}
