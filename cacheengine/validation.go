package cacheengine

import (
	"strings"
	"unicode"
)

func validIdentity(value string, maximum int, allowEmpty bool) bool {
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

func validNativeIdentity(request NativeRequest) bool {
	return validIdentity(request.Scope, 4096, false) &&
		validIdentity(request.Epoch, 4096, false) &&
		validIdentity(request.PartitionKey, 4096, true) &&
		validIdentity(request.Provider, 64, false) &&
		validIdentity(request.Model, 512, true) &&
		validIdentity(request.Region, 64, true) &&
		validIdentity(request.Endpoint, 512, true) &&
		validIdentity(request.RuntimeMode, 128, true) &&
		validIdentity(request.AuthMode, 128, true)
}
