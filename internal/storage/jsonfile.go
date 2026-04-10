package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type JSONFile[T any] struct {
	path     string
	defaults T
}

func NewJSONFile[T any](path string, defaults T) *JSONFile[T] {
	return &JSONFile[T]{
		path:     path,
		defaults: defaults,
	}
}

func (f *JSONFile[T]) Load() (T, error) {
	return LoadJSONFile(f.path, f.defaults)
}

func (f *JSONFile[T]) Save(value T) error {
	return SaveJSONFile(f.path, value)
}

func LoadJSONFile[T any](path string, defaults T) (T, error) {
	value := defaults

	data, sourcePath, err := readPrimaryOrBackup(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaults, nil
		}

		var zero T
		return zero, fmt.Errorf("读取 JSON 文件失败: %w", err)
	}

	if err := json.Unmarshal(data, &value); err != nil {
		backupPath := backupPathFor(path)
		if sourcePath != backupPath {
			backupData, backupErr := os.ReadFile(backupPath)
			if backupErr == nil {
				value = defaults
				if restoreErr := json.Unmarshal(backupData, &value); restoreErr == nil {
					return value, nil
				}
			}
		}

		var zero T
		return zero, fmt.Errorf("解析 JSON 文件 %q 失败: %w", sourcePath, err)
	}

	return value, nil
}

func SaveJSONFile(path string, value any) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 JSON 失败: %w", err)
	}
	data = append(data, '\n')

	tempFile, err := os.CreateTemp(directory, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}

	tempPath := tempFile.Name()
	defer func() {
		_ = os.Remove(tempPath)
	}()

	if _, err := tempFile.Write(data); err != nil {
		_ = tempFile.Close()
		return fmt.Errorf("写入临时文件失败: %w", err)
	}

	if err := tempFile.Sync(); err != nil {
		_ = tempFile.Close()
		return fmt.Errorf("同步临时文件失败: %w", err)
	}

	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}

	backupPath := backupPathFor(path)
	if err := rotateToBackup(path, backupPath); err != nil {
		return err
	}

	if err := os.Rename(tempPath, path); err != nil {
		_ = restoreFromBackup(path, backupPath)
		return fmt.Errorf("替换 JSON 文件失败: %w", err)
	}

	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("清理备份文件失败: %w", err)
	}

	return nil
}

func readPrimaryOrBackup(path string) ([]byte, string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, path, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}

	backupPath := backupPathFor(path)
	backupData, backupErr := os.ReadFile(backupPath)
	if backupErr == nil {
		return backupData, backupPath, nil
	}
	if errors.Is(backupErr, os.ErrNotExist) {
		return nil, "", os.ErrNotExist
	}

	return nil, "", backupErr
}

func rotateToBackup(path string, backupPath string) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("检查现有文件失败: %w", err)
	}

	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除旧备份失败: %w", err)
	}

	if err := os.Rename(path, backupPath); err != nil {
		return fmt.Errorf("创建备份文件失败: %w", err)
	}

	return nil
}

func restoreFromBackup(path string, backupPath string) error {
	if _, err := os.Stat(backupPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	_ = os.Remove(path)
	return os.Rename(backupPath, path)
}

func backupPathFor(path string) string {
	return path + ".bak"
}
