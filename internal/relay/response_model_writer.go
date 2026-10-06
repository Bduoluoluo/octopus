package relay

import (
	"encoding/json"
	"io"
)

type responseModelWriter struct {
	destination  io.Writer
	model        string
	observe      func(string)
	depth        int
	inString     bool
	escaped      bool
	keyNext      bool
	modelNext    bool
	captureKey   bool
	captureModel bool
	pending      []byte
}

func (writer *responseModelWriter) Write(data []byte) (int, error) {
	output := make([]byte, 0, len(data)+len(writer.model))
	for _, char := range data {
		if writer.inString {
			capturing := writer.captureKey || writer.captureModel
			if capturing {
				writer.pending = append(writer.pending, char)
			} else {
				output = append(output, char)
			}
			if writer.escaped {
				writer.escaped = false
			} else if char == '\\' {
				writer.escaped = true
			} else if char == '"' {
				writer.inString = false
				if capturing {
					var value string
					if json.Unmarshal(writer.pending, &value) == nil {
						if writer.captureKey {
							writer.modelNext = value == "model" || value == "modelVersion"
						} else {
							if writer.observe != nil {
								writer.observe(value)
							}
							if writer.model != "" {
								writer.pending, _ = json.Marshal(writer.model)
							}
						}
					}
					output = append(output, writer.pending...)
					writer.pending = nil
				}
				writer.captureKey, writer.captureModel = false, false
			}
			if len(writer.pending) > 4096 {
				output = append(output, writer.pending...)
				writer.pending = nil
				writer.captureKey, writer.captureModel, writer.modelNext = false, false, false
			}
			continue
		}
		if char == '"' {
			writer.inString = true
			writer.captureKey = writer.depth == 1 && writer.keyNext
			writer.captureModel = writer.depth == 1 && !writer.keyNext && writer.modelNext
			writer.keyNext, writer.modelNext = false, false
			if writer.captureKey || writer.captureModel {
				writer.pending = append(writer.pending, char)
			} else {
				output = append(output, char)
			}
			continue
		}
		output = append(output, char)
		switch char {
		case '{', '[':
			writer.depth++
			writer.keyNext = writer.depth == 1 && char == '{'
			writer.modelNext = false
		case '}', ']':
			writer.depth--
			writer.keyNext, writer.modelNext = false, false
		case ',':
			writer.keyNext = writer.depth == 1
			writer.modelNext = false
		case ':', ' ', '\t', '\r', '\n':
		default:
			writer.modelNext = false
		}
	}
	if len(output) > 0 {
		count, err := writer.destination.Write(output)
		if err != nil {
			return 0, err
		}
		if count != len(output) {
			return 0, io.ErrShortWrite
		}
	}
	return len(data), nil
}

func (writer *responseModelWriter) finish() error {
	if len(writer.pending) == 0 {
		return nil
	}
	_, err := writer.destination.Write(writer.pending)
	writer.pending = nil
	return err
}
