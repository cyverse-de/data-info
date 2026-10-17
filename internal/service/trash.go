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

// trashSuffixLength is how many characters are appended to a name in the trash.
const trashSuffixLength = 7

// trashSuffixAlphabet is alphanumeric because the suffix becomes part of a path.
const trashSuffixAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// TrashPathFor names where something will land in a user's trash; a random suffix keeps
// same-named items from different collections from colliding.
func TrashPathFor(layout paths.Layout, user, path string) (string, error) {
	suffix, err := randomSuffix()
	if err != nil {
		return "", err
	}
	return paths.Join(layout.UserTrash(user), paths.Base(path)+"."+suffix), nil
}

// randomSuffix returns the characters appended to a trashed name, from crypto/rand so
// replicas deleting at the same moment cannot produce the same suffix.
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

	// PartialRestore is true when it is going to the user's home instead of where it came
	// from.
	PartialRestore bool `json:"partial-restore"`
}

// RestoreStore is what planning a restore needs.
type RestoreStore interface {
	Stat(ctx context.Context, path string) *lazy.Value[rods.Stat]
	AVUs(ctx context.Context, path string) *lazy.Value[[]rods.AVU]
}

// PlanRestore decides where a trashed item goes back to: its recorded origin, or the user's
// home when no origin was recorded or the user can no longer write to the origin's parent,
// with a number appended if the name is taken. view acts as the user and admin as the
// service account; existence is checked as admin so paths the user cannot see still count.
func PlanRestore(ctx context.Context, view, admin RestoreStore, layout paths.Layout, user, path string) (RestorePlan, error) {
	origin, err := trashOrigin(ctx, view, path)
	if err != nil {
		return RestorePlan{}, err
	}

	toHome := origin == ""
	if !toHome {
		toHome, err = parentUnwriteable(ctx, view, admin, paths.Dir(origin))
		if err != nil {
			return RestorePlan{}, err
		}
	}

	if toHome {
		origin = paths.Join(layout.UserHome(user), paths.Base(path))
	}

	free, err := firstFreeName(ctx, admin, origin)
	if err != nil {
		return RestorePlan{}, err
	}

	return RestorePlan{RestoredPath: free, PartialRestore: toHome}, nil
}

// parentUnwriteable reports whether a restore's parent exists but the user cannot write to it.
// A parent that no longer exists is not a reason to divert: the job recreates it.
func parentUnwriteable(ctx context.Context, view, admin RestoreStore, parent string) (bool, error) {
	exists, err := admin.Stat(ctx, parent).Get(ctx)
	if err != nil {
		return false, err
	}
	if !exists.Exists {
		return false, nil
	}

	held, err := view.Stat(ctx, parent).Get(ctx)
	if err != nil {
		return false, err
	}
	return !rods.Permits(held.Permission, rods.PermissionWrite), nil
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

	// Unbounded: stopping early would fail a restore that could have succeeded.
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
