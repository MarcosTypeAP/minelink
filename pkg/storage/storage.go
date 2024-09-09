package storage

import (
	"encoding/json"
	"fmt"
	"github/MarcosTypeAP/minelink/pkg/common"
	"io"
	"os"
	"sync"
)

var usedFiles = map[string]bool{}

type Store[T any] interface {
	Load() (T, error)
	Save(T) error
}

type JsonStore[T any] struct {
	filepath string
	mu       sync.Mutex
}

func NewJsonStore[T any](filename string, temp bool) (*JsonStore[T], error) {
	store := new(JsonStore[T])
	if temp {
		store.filepath = common.GetTempPath(filename)
	} else {
		store.filepath = common.GetConfigPath(filename)
	}

	if usedFiles[store.filepath] {
		return nil, fmt.Errorf("filepath already used: %q", store.filepath)
	}
	usedFiles[store.filepath] = true

	return store, nil
}

func (s *JsonStore[T]) Load() (T, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var value T

	file, err := os.Open(s.filepath)
	if err != nil {
		if os.IsNotExist(err) {
			return value, nil
		}
		return value, fmt.Errorf("json store: opening file: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	if err = decoder.Decode(&value); err != nil {
		if err == io.EOF {
			return value, nil
		}
		return value, fmt.Errorf("json store: decoding %T: %w", value, err)
	}
	return value, nil
}

func (s *JsonStore[T]) Save(value T) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := os.OpenFile(s.filepath, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("json store: opening file: %w", err)
	}
	defer file.Close()

	bufFile, err := os.CreateTemp("", "minelink-json-encoding-buf-*")
	if err != nil {
		return fmt.Errorf("json store: creating buffer file: %w", err)
	}
	defer func() {
		bufFile.Close()
		err := os.Remove(bufFile.Name())
		if err != nil {
			panic(err)
		}
	}()

	encoder := json.NewEncoder(bufFile)
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("json store: encoding %T: %w", value, err)
	}
	if _, err := bufFile.Seek(0, 0); err != nil {
		return fmt.Errorf("json store: setting the buffer file's offset: %w", err)
	}

	if _, err := io.Copy(file, bufFile); err != nil {
		return fmt.Errorf("json store: copying from buffer file: %w", err)
	}
	return nil
}
