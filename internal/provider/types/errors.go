package types

import "errors"

var (
	ErrProviderUnavailable = errors.New("provider unavailable")
	ErrProviderNotFound    = errors.New("provider resource not found")
	ErrProviderConflict    = errors.New("provider state conflict")
	ErrProviderTimeout     = errors.New("provider operation timeout")
	ErrProviderDenied      = errors.New("provider operation denied")
	ErrProviderInternal    = errors.New("provider internal error")
)
