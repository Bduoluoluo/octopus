package stream

import (
	"bufio"
	"context"
	"io"
	"sync"
)

type FramedSSESource struct {
	reader  io.ReadCloser
	scanner *bufio.Scanner
	once    sync.Once
	err     error
}

func NewFramedSSESource(reader io.ReadCloser, maxSize int) *FramedSSESource {
	if maxSize <= 0 {
		maxSize = 32 * 1024 * 1024
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, min(4096, maxSize)), maxSize)
	scanner.Split(splitSSEFrame)
	return &FramedSSESource{reader: reader, scanner: scanner}
}

func splitSSEFrame(data []byte, atEOF bool) (int, []byte, error) {
	lineStart := 0
	for index := 0; index < len(data); index++ {
		if data[index] != '\r' && data[index] != '\n' {
			continue
		}
		end := index + 1
		if data[index] == '\r' {
			if end == len(data) && !atEOF {
				return 0, nil, nil
			}
			if end < len(data) && data[end] == '\n' {
				end++
			}
		}
		if index == lineStart {
			return end, data[:end], nil
		}
		lineStart = end
		index = end - 1
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func (source *FramedSSESource) ReadEvent(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source.scanner.Scan() {
		return append([]byte(nil), source.scanner.Bytes()...), nil
	}
	if err := source.scanner.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func (source *FramedSSESource) Close() error {
	source.once.Do(func() { source.err = source.reader.Close() })
	return source.err
}
