package store

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"
)

func TestRepositoryCorruptionRecognizesMissingPackOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		err  error
		want bool
	}{
		{"relative pack", "objects/pack/pack-abc.pack", fs.ErrNotExist, true},
		{"absolute pack", "/repo/.git/objects/pack/pack-abc.pack", fs.ErrNotExist, true},
		{"permission failure", "objects/pack/pack-abc.pack", fs.ErrPermission, false},
		{"config missing", "config/config.yaml", fs.ErrNotExist, false},
		{"unrelated pack", "downloads/pack-abc.pack", fs.ErrNotExist, false},
		{"non-pack object", "objects/pack/unrelated.pack", fs.ErrNotExist, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fmt.Errorf("packhandle: pack size: %w", &fs.PathError{Op: "statat", Path: tc.path, Err: tc.err})
			if got := isRepositoryCorruptionError(err); got != tc.want {
				t.Fatalf("corruption classification = %v, want %v: %v", got, tc.want, err)
			}
		})
	}
	if isRepositoryCorruptionError(nil) || isRepositoryCorruptionError(errors.New("unknown failure")) {
		t.Fatal("unrelated errors must not trigger repository recovery")
	}
}
