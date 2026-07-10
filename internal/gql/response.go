package gql

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

func (c *Client) handleResponses(responses []Response, allowSingleRetry bool) (retry bool, consumeSingleRetry bool, minimumDelay time.Duration, err error) {
	for index := range responses {
		response := &responses[index]
		if len(response.Errors) > 0 {
			if hasOnlyNonFatalErrors(*response) && response.Error == "" {
				c.logger.Warn("GQL 响应带 failed integrity check，已容忍并返回部分数据",
					"operation", operationName(*response))
				continue
			}
			allHandled := true
			for _, responseError := range response.Errors {
				switch responseError.Message {
				case "service error", "PersistedQueryNotFound":
					if allowSingleRetry {
						return true, true, forcedRetryDelay, nil
					}
					allHandled = false
				case "server error":
					if err := nullifyPath(response, responseError.Path); err != nil {
						return false, false, 0, newRequestError(*response)
					}
				case "service timeout", "service unavailable", "context deadline exceeded":
					return true, false, 0, nil
				default:
					allHandled = false
				}
			}
			if !allHandled {
				return false, false, 0, newRequestError(*response)
			}
		}
		if response.Error != "" {
			return false, false, 0, &RequestError{
				Operation: operationName(*response),
				Message:   fmt.Sprintf("%s: %s", response.Error, response.Message),
			}
		}
	}

	return false, false, 0, nil
}

func hasOnlyNonFatalErrors(response Response) bool {
	if response.Data == nil || len(response.Errors) == 0 {
		return false
	}

	for _, responseError := range response.Errors {
		if responseError.Message != "failed integrity check" {
			return false
		}
	}
	return true
}

func validateBatchResponses(operations []Operation, responses []Response) error {
	if len(responses) != len(operations) {
		return fmt.Errorf("GQL batch 响应数量不匹配: 请求 %d 个，响应 %d 个", len(operations), len(responses))
	}
	for index := range responses {
		got := operationName(responses[index])
		want := operations[index].OperationName
		if got != "" && want != "" && got != want {
			return fmt.Errorf("GQL batch operationName 不匹配: index=%d want=%q got=%q", index, want, got)
		}
	}

	return nil
}

func newRequestError(response Response) error {
	message := "GQL 请求返回错误"
	if len(response.Errors) > 0 {
		message = response.Errors[0].Message
	}

	return &RequestError{
		Operation: operationName(response),
		Message:   message,
		Errors:    append([]ResponseError(nil), response.Errors...),
	}
}

func operationName(response Response) string {
	if response.Extensions == nil {
		return ""
	}

	value, ok := response.Extensions["operationName"]
	if !ok {
		return ""
	}

	name, _ := value.(string)
	return name
}

func (r *Response) decode(body []byte) error {
	if err := json.Unmarshal(body, r); err != nil {
		return fmt.Errorf("解析 GQL 响应失败: %w", err)
	}

	return nil
}

func nullifyPath(response *Response, path []any) error {
	if response == nil {
		return fmt.Errorf("GQL 响应为空")
	}
	if len(path) == 0 {
		return fmt.Errorf("server error 缺少错误路径")
	}

	current := response.Data
	for _, step := range path[:len(path)-1] {
		next, err := navigate(current, step)
		if err != nil {
			return err
		}
		current = next
	}

	return assignNil(current, path[len(path)-1])
}

func navigate(current any, step any) (any, error) {
	switch typed := current.(type) {
	case map[string]any:
		key, ok := step.(string)
		if !ok {
			return nil, fmt.Errorf("错误路径包含非法对象键: %v", step)
		}
		next, ok := typed[key]
		if !ok {
			return nil, fmt.Errorf("错误路径中的对象键不存在: %s", key)
		}
		return next, nil
	case []any:
		index, ok := pathIndex(step)
		if !ok {
			return nil, fmt.Errorf("错误路径包含非法数组索引: %v", step)
		}
		if index < 0 || index >= len(typed) {
			return nil, fmt.Errorf("错误路径中的数组索引越界: %d", index)
		}
		return typed[index], nil
	default:
		return nil, fmt.Errorf("错误路径无法继续下钻，当前节点类型为 %T", current)
	}
}

func assignNil(current any, step any) error {
	switch typed := current.(type) {
	case map[string]any:
		key, ok := step.(string)
		if !ok {
			return fmt.Errorf("错误路径包含非法对象键: %v", step)
		}
		typed[key] = nil
		return nil
	case []any:
		index, ok := pathIndex(step)
		if !ok {
			return fmt.Errorf("错误路径包含非法数组索引: %v", step)
		}
		if index < 0 || index >= len(typed) {
			return fmt.Errorf("错误路径中的数组索引越界: %d", index)
		}
		typed[index] = nil
		return nil
	default:
		return fmt.Errorf("错误路径无法写入 nil，当前节点类型为 %T", current)
	}
}

func pathIndex(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		if math.Trunc(typed) != typed {
			return 0, false
		}
		return int(typed), true
	default:
		return 0, false
	}
}

func IsRequestError(err error) bool {
	var requestError *RequestError
	return errors.As(err, &requestError)
}

func (e *RequestError) Error() string {
	if e == nil {
		return ""
	}
	if e.Operation != "" {
		return fmt.Sprintf("%s: %s", e.Operation, e.Message)
	}

	return e.Message
}
