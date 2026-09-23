package probe

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// LoadTargets decodes a JSON array of targets and validates each one.
// Unknown fields are rejected so a typo like "timout_ms" fails loudly
// instead of being silently ignored.
func LoadTargets(r io.Reader) ([]Target, error) {
	var ts []Target
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ts); err != nil {
		return nil, fmt.Errorf("parse targets: %w", err)
	}
	for i := range ts {
		if err := ts[i].normalize(); err != nil {
			return nil, fmt.Errorf("target %d: %w", i, err)
		}
	}
	return ts, nil
}

// FromURL builds a target from a bare URL given on the command line.
func FromURL(raw string) (Target, error) {
	t := Target{URL: raw}
	err := t.normalize()
	return t, err
}

func (t *Target) normalize() error {
	t.URL = strings.TrimSpace(t.URL)
	if t.URL == "" {
		return errors.New("url is required")
	}
	u, err := url.Parse(t.URL)
	if err != nil {
		return fmt.Errorf("invalid url %q: %w", t.URL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url %q: scheme must be http or https", t.URL)
	}
	if u.Host == "" {
		return fmt.Errorf("url %q: missing host", t.URL)
	}
	if t.ExpectStatus != 0 && (t.ExpectStatus < 100 || t.ExpectStatus > 599) {
		return fmt.Errorf("url %q: expect_status %d is not a valid HTTP status", t.URL, t.ExpectStatus)
	}
	if t.TimeoutMS < 0 {
		return fmt.Errorf("url %q: timeout_ms must not be negative", t.URL)
	}
	if t.Name == "" {
		// Include the path so two checks on one host stay distinguishable.
		t.Name = u.Host + strings.TrimSuffix(u.Path, "/")
	}
	return nil
}
