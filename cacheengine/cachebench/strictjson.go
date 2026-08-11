package cachebench

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
)

func validBoundedText(value string, maximum int, allowEmpty bool) bool {
	if value != strings.TrimSpace(value) || len(value) > maximum || !allowEmpty && value == "" {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func validUniqueJSONObject(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := validateUniqueJSONValue(decoder, true, 0); err != nil {
		return false
	}
	_, err := decoder.Token()
	return errors.Is(err, io.EOF)
}

func validateUniqueJSONValue(decoder *json.Decoder, root bool, depth int) error {
	if depth > 512 {
		return errors.New("cachebench: JSON nesting limit exceeded")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		if root {
			return errors.New("cachebench: JSON root must be object")
		}
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return errors.New("cachebench: duplicate or invalid object key")
			}
			seen[key] = true
			if err := validateUniqueJSONValue(decoder, false, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("cachebench: invalid object close")
		}
		return nil
	case '[':
		if root {
			return errors.New("cachebench: JSON root must be object")
		}
		for decoder.More() {
			if err := validateUniqueJSONValue(decoder, false, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("cachebench: invalid array close")
		}
		return nil
	default:
		return errors.New("cachebench: unexpected JSON delimiter")
	}
}
