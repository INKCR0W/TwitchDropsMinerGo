package gql

import (
	"encoding/json"
	"fmt"
)

type requiredValue struct {
	name string
}

func Required(name string) any {
	return requiredValue{name: name}
}

func (r requiredValue) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("变量 %q 尚未赋值", r.name)
}

type Operation struct {
	OperationName string              `json:"operationName"`
	Extensions    OperationExtensions `json:"extensions"`
	Variables     map[string]any      `json:"variables,omitempty"`
}

type RawQuery struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type OperationExtensions struct {
	PersistedQuery PersistedQuery `json:"persistedQuery"`
}

type PersistedQuery struct {
	Version    int    `json:"version"`
	SHA256Hash string `json:"sha256Hash"`
}

func NewOperation(name string, sha256Hash string, variables map[string]any) Operation {
	operation := Operation{
		OperationName: name,
		Extensions: OperationExtensions{
			PersistedQuery: PersistedQuery{
				Version:    1,
				SHA256Hash: sha256Hash,
			},
		},
	}
	if len(variables) > 0 {
		operation.Variables = cloneMap(variables)
	}

	return operation
}

func (o Operation) Clone() Operation {
	cloned := o
	if len(o.Variables) > 0 {
		cloned.Variables = cloneMap(o.Variables)
	}

	return cloned
}

func (o Operation) WithVariables(variables map[string]any) (Operation, error) {
	cloned := o.Clone()
	if len(cloned.Variables) == 0 {
		cloned.Variables = make(map[string]any)
	}
	if err := mergeVariables(cloned.Variables, variables); err != nil {
		return Operation{}, err
	}
	if err := cloned.Validate(); err != nil {
		return Operation{}, err
	}

	return cloned, nil
}

func (o Operation) MustWithVariables(variables map[string]any) Operation {
	operation, err := o.WithVariables(variables)
	if err != nil {
		panic(err)
	}

	return operation
}

func (o Operation) Validate() error {
	return validateValue("variables", o.Variables)
}

func cloneMap(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = cloneValue(value)
	}

	return cloned
}

func cloneSlice(source []any) []any {
	cloned := make([]any, len(source))
	for index, value := range source {
		cloned[index] = cloneValue(value)
	}

	return cloned
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		return cloneSlice(typed)
	case requiredValue:
		return typed
	default:
		return typed
	}
}

func mergeVariables(baseVars map[string]any, variables map[string]any) error {
	for key, value := range variables {
		current, exists := baseVars[key]
		if !exists {
			baseVars[key] = cloneValue(value)
			continue
		}

		incomingMap, incomingIsMap := value.(map[string]any)
		currentMap, currentIsMap := current.(map[string]any)
		_, currentRequired := current.(requiredValue)

		switch {
		case incomingIsMap && currentIsMap:
			if err := mergeVariables(currentMap, incomingMap); err != nil {
				return err
			}
		case incomingIsMap && currentRequired:
			baseVars[key] = cloneMap(incomingMap)
		case incomingIsMap:
			return fmt.Errorf("变量 %q 类型冲突：传入值是对象，但模板值不是对象", key)
		case currentIsMap:
			return fmt.Errorf("变量 %q 类型冲突：模板值是对象，但传入值不是对象", key)
		default:
			baseVars[key] = cloneValue(value)
		}
	}

	return nil
}

func validateValue(path string, value any) error {
	switch typed := value.(type) {
	case nil:
		return nil
	case requiredValue:
		return fmt.Errorf("%s 中缺少必填变量 %q", path, typed.name)
	case map[string]any:
		for key, nested := range typed {
			if err := validateValue(path+"."+key, nested); err != nil {
				return err
			}
		}
	case []any:
		for index, nested := range typed {
			if err := validateValue(fmt.Sprintf("%s[%d]", path, index), nested); err != nil {
				return err
			}
		}
	default:
		_, _ = json.Marshal(typed)
	}

	return nil
}
