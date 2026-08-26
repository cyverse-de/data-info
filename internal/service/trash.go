package service

import (
	"context"
	"crypto/rand"
	"fmt"

	"github.com/cyverse-de/data-info/internal/lazy"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
)

// TrashOriginAttribute records where something was before it was trashed, so that restoring
// it can put it back. It is written with SystemUnit, the DE's marker for metadata it manages
// itself.
const TrashOriginAttribute = "ipc-trash-origin"

// trashSuffixLength is how many characters are appended to a name in the trash. Two things
// with the same name deleted from different collections would otherwise collide.
const trashSuffixLength = 7

// trashSuffixAlphabet is what those characters are drawn from. The Clojure service uses the
// alphanumerics and nothing else, and the value ends up in a path, so it stays that way.
const trashSuffixAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// TrashPathFor names where something will land in a user's trash.
//
// The suffix makes the name unique rather than meaningful: two files called results.csv
// deleted from two collections both want to be results.csv in the trash, and only one of
// them can be.
func TrashPathFor(layout paths.Layout, user, path string) (string, error) {
	suffix, err := randomSuffix()
	if err != nil {
		return "", err
	}
	return paths.Join(layout.UserTrash(user), paths.Base(path)+"."+suffix), nil
}

// randomSuffix returns the characters appended to a trashed name.
//
// From crypto/rand rather than math/rand. Not because this is a secret, but because two
// replicas deleting at the same moment must not produce the same suffix, and a
// seeded-from-the-clock generator in two processes can.
func randomSuffix() (string, error) {
	buf := make([]byte, trashSuffixLength)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating a trash suffix: %w", err)
	}

	out := make([]byte, trashSuffixLength)
	for i, b := range buf {
		out[i] = trashSuffixAlphabet[int(b)%len(trashSuffixAlphabet)]
	}
	return string(out), nil
}

// RestorePlan is where one trashed item will be put back, and whether that is where it came
// from.
type RestorePlan struct {
	// RestoredPath is where it will end up.
	RestoredPath string `json:"restored-path"`

	// PartialRestore is true when it could not go back where it came from and is going to
	// the user's home instead. The DE tells the user, because it is not what they asked
	// for.
	PartialRestore bool `json:"partial-restore"`
}

// RestoreStore is what planning a restore needs.
type RestoreStore interface {
	Stat(ctx context.Context, path string) *lazy.Value[rods.Stat]
	AVUs(ctx context.Context, path string) *lazy.Value[[]rods.AVU]
}

// PlanRestore decides where a trashed item goes back to.
//
// It goes back where it came from when that is still possible. It is not possible when the
// origin was never recorded, or when the collection it came from still exists but the user
// can no longer write to it -- which happens when something was shared with them and then
// unshared. In those cases it goes to their home instead, and the plan says so.
//
// A name already in use is not a conflict: a number is appended until one is free, so
// restoring never overwrites.
func PlanRestore(ctx context.Context, store RestoreStore, layout paths.Layout, user, path string) (RestorePlan, error) {
	origin, err := trashOrigin(ctx, store, path)
	if err != nil {
		return RestorePlan{}, err
	}

	toHome := origin == ""
	if !toHome {
		parent := paths.Dir(origin)

		stat, err := store.Stat(ctx, parent).Get(ctx)
		if err != nil {
			return RestorePlan{}, err
		}
		// A parent that no longer exists is not a reason to give up: it is recreated
		// below. A parent that exists and cannot be written to is.
		if stat.Exists && !rods.Permits(stat.Permission, rods.PermissionWrite) {
			toHome = true
		}
	}

	if toHome {
		origin = paths.Join(layout.UserHome(user), paths.Base(path))
	}

	free, err := firstFreeName(ctx, store, origin)
	if err != nil {
		return RestorePlan{}, err
	}

	return RestorePlan{RestoredPath: free, PartialRestore: toHome}, nil
}

// trashOrigin reads where something was before it was trashed, or "" when nothing recorded it.
func trashOrigin(ctx context.Context, store RestoreStore, path string) (string, error) {
	avus, err := store.AVUs(ctx, path).Get(ctx)
	if err != nil {
		return "", err
	}

	for _, avu := range avus {
		if avu.Attribute == TrashOriginAttribute {
			return avu.Value, nil
		}
	}
	return "", nil
}

// firstFreeName appends a number to a path until nothing is there.
func firstFreeName(ctx context.Context, store RestoreStore, path string) (string, error) {
	stat, err := store.Stat(ctx, path).Get(ctx)
	if err != nil {
		return "", err
	}
	if !stat.Exists {
		return path, nil
	}

	// Unbounded in the Clojure service too. A collection holding thousands of restores of the
	// same name would be pathological, and stopping early would mean failing a restore that
	// could have succeeded.
	for attempt := 0; ; attempt++ {
		candidate := fmt.Sprintf("%s.%d", path, attempt)

		stat, err := store.Stat(ctx, candidate).Get(ctx)
		if err != nil {
			return "", err
		}
		if !stat.Exists {
			return candidate, nil
		}
	}
}
