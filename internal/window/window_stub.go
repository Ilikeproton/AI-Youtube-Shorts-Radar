//go:build !windows

package window

import (
	"errors"

	"youtubeshort/internal/config"
)

func Open(_ string, _ config.App) error {
	return errors.New("windows host is only available on Windows")
}
