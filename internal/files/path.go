package files

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

var ErrInvalidPath = errors.New("invalid workspace path")

func NormalizeWorkspaceRelativePath(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("%w: path must not be empty", ErrInvalidPath)
	}
	if strings.ContainsRune(raw, rune(0)) {
		return "", fmt.Errorf("%w: path must not contain null bytes", ErrInvalidPath)
	}
	if path.IsAbs(raw) {
		return "", fmt.Errorf("%w: absolute paths are not allowed", ErrInvalidPath)
	}

	clean := path.Clean(raw)
	if clean == "." || clean == "" {
		return "", fmt.Errorf("%w: path must target a file or directory under workspace root", ErrInvalidPath)
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: path escapes workspace root", ErrInvalidPath)
	}

	return clean, nil
}
