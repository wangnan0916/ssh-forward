package app

import (
	"errors"
	"os"
	"path/filepath"
)

func stripJSONC(input []byte) ([]byte, error) {
	output := make([]byte, 0, len(input))
	inString := false
	inLineComment := false
	inBlockComment := false
	for index := 0; index < len(input); index++ {
		current := input[index]
		switch {
		case inLineComment:
			if current == '\n' {
				inLineComment = false
				output = append(output, current)
			}
			continue
		case inBlockComment:
			if current == '*' && index+1 < len(input) && input[index+1] == '/' {
				inBlockComment = false
				index++
			}
			continue
		case inString:
			output = append(output, current)
			if current == '\\' && index+1 < len(input) {
				index++
				output = append(output, input[index])
				continue
			}
			if current == '"' {
				inString = false
			}
			continue
		}
		switch current {
		case '"':
			inString = true
			output = append(output, current)
		case '/':
			if index+1 < len(input) && input[index+1] == '/' {
				inLineComment = true
				index++
			} else if index+1 < len(input) && input[index+1] == '*' {
				inBlockComment = true
				index++
			} else {
				output = append(output, current)
			}
		case ',':
			next := index + 1
			for next < len(input) && (input[next] == ' ' || input[next] == '\t' || input[next] == '\n' || input[next] == '\r') {
				next++
			}
			if next < len(input) && (input[next] == ']' || input[next] == '}') {
				continue
			}
			output = append(output, current)
		default:
			output = append(output, current)
		}
	}
	if inString {
		return nil, errors.New("jsonc: unterminated string")
	}
	if inBlockComment {
		return nil, errors.New("jsonc: unterminated block comment")
	}
	return output, nil
}

func writeAtomic(path string, data []byte, tmpPattern string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), tmpPattern)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
