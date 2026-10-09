package shared

import (
	"bytes"
	"encoding/json"
	"errors"
)

var ErrInvalidRequest = errors.New("invalid protocol request")

func To[Value any](data []byte) (Value, error) {
	var value Value
	err := json.Unmarshal(data, &value)
	return value, err
}

func IsNull(data []byte) bool {
	return bytes.Equal(bytes.TrimSpace(data), []byte("null"))
}
