package catalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/BurntSushi/toml"

	"github.com/getdovy/dovy/internal/apt"
)

// MinimumFile names the oldest dovy that can read a release (ADR 0023).
const MinimumFile = "catalog.toml"

// Running is this dovy's version, set by main. One that is not X.Y.Z (a
// test or development build) meets any minimum.
var Running = ""

var releaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// minimum reads the dovy field of a catalog.toml. Other fields are
// ignored, so the file can grow without breaking older versions.
func minimum(data []byte) (string, error) {
	var f struct {
		Dovy string `toml:"dovy"`
	}
	if _, err := toml.Decode(string(data), &f); err != nil {
		return "", fmt.Errorf("%s: %w", MinimumFile, err)
	}
	if !releaseVersion.MatchString(f.Dovy) {
		return "", fmt.Errorf(`%s: dovy must be "X.Y.Z", not %q`, MinimumFile, f.Dovy)
	}
	return f.Dovy, nil
}

// readable is "" when this dovy can read a release whose catalog.toml
// holds data, else why not.
func readable(data []byte) string {
	min, err := minimum(data)
	if err != nil {
		return err.Error()
	}
	if releaseVersion.MatchString(Running) && apt.Compare(Running, min) < 0 {
		return fmt.Sprintf("needs dovy %s (this is %s)", min, Running)
	}
	return ""
}

// TooNew: the local copy needs a newer dovy than this one.
type TooNew string

func (e TooNew) Error() string { return string(e) }

// checkMinimum is Verify's check of the local copy in dir.
func checkMinimum(dir, tag string) error {
	b, err := os.ReadFile(filepath.Join(dir, MinimumFile))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s has no %s", tag, MinimumFile)
	}
	if err != nil {
		return err
	}
	if _, err := minimum(b); err != nil {
		return err
	}
	if why := readable(b); why != "" {
		return TooNew(tag + " " + why)
	}
	return nil
}
