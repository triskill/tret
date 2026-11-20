package journal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// gateTimeout bounds how long a worker waits for the lock which
	// serializes the changes of a claim.
	gateTimeout = 30 * time.Second
)

// File keeps the results of the steps in a directory, so they survive the
// restart of a worker and are shared by the workers of one node.
//
// A claim is a file which only its owner may create. Every change of that
// file — taking it, taking an expired one over, releasing it — happens under
// a lock, an OS file lock on a file which is never removed. Without it a
// worker which takes an expired claim over could remove the claim another
// worker has created in the meantime, and both would run the work.
//
// The result is written to a temporary file and renamed, so a reader never
// sees half of it.
//
// The directory must be on a filesystem of one node. Several nodes need a
// store which all of them can reach, and which decides the winner of a claim
// in one place.
type File struct {
	dir string
}

// NewFile returns a journal in the given directory, which is created if it
// does not exist.
func NewFile(dir string) (*File, error) {
	if dir == "" {
		return nil, fmt.Errorf("journal directory is required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("journal directory %s: %w", dir, err)
	}
	return &File{dir: dir}, nil
}

// Dir returns the directory the journal keeps its files in.
func (f *File) Dir() string { return f.dir }

func (f *File) name(key Key) string {
	name := fmt.Sprintf("job-%d", key.JobKey)
	if key.Business != "" {
		sum := sha256.Sum256([]byte(key.Business))
		name += "-" + hex.EncodeToString(sum[:6])
	}
	return name
}

func (f *File) resultPath(key Key) string { return filepath.Join(f.dir, f.name(key)+".json") }

func (f *File) claimPath(key Key) string { return filepath.Join(f.dir, f.name(key)+".claim") }

func (f *File) Result(_ context.Context, key Key) (map[string]any, bool, error) {
	data, err := os.ReadFile(f.resultPath(key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, false, fmt.Errorf("read the stored result of %s: %w", key.String(), err)
	}
	return result, true, nil
}

// Acquire claims the step for the owner until the lease expires.
//
// Every change of the claim happens under the gate, so a worker which takes
// an expired claim over cannot remove the claim another worker has created
// in the meantime.
func (f *File) Acquire(_ context.Context, key Key, owner string, lease time.Duration) (bool, error) {
	release, err := f.gate(key)
	if err != nil {
		return false, err
	}
	defer release()
	path := f.claimPath(key)
	claim, held, err := readClaim(path)
	if err != nil {
		return false, err
	}
	if held && !claim.expired(path, lease) {
		// A live claim is refused whoever holds it: the same worker may
		// hold the step in another delivery of the same job.
		return false, nil
	}
	// The claim is free or expired: take it.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	return f.createClaim(path, owner, lease)
}

// createClaim creates the claim file, which only one worker may do. It is
// called under the gate.
func (f *File) createClaim(path, owner string, lease time.Duration) (bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		return false, err
	}
	// The claim carries the moment it expires: the worker which holds it
	// promised to hold it until then, and a worker which finds the claim
	// past that moment may take it over.
	content := owner + "\n" + strconv.FormatInt(time.Now().Add(lease).UnixNano(), 10)
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		os.Remove(path)
		return false, err
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return false, err
	}
	return true, nil
}

// Release drops the claim of the owner.
func (f *File) Release(_ context.Context, key Key, owner string) error {
	release, err := f.gate(key)
	if err != nil {
		return err
	}
	defer release()
	path := f.claimPath(key)
	claim, held, err := readClaim(path)
	if err != nil {
		return err
	}
	if !held || claim.owner != owner {
		// Another worker holds the claim now; its file must stay.
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Claim returns the claim on the step.
func (f *File) Claim(_ context.Context, key Key) (ClaimInfo, bool, error) {
	claim, held, err := readClaim(f.claimPath(key))
	if err != nil || !held {
		return ClaimInfo{}, held, err
	}
	return ClaimInfo{Owner: claim.owner, Until: claim.until}, true, nil
}

// gate serializes the changes of the claim of a step.
//
// The gate is an OS file lock on a file which is never removed. The
// operating system arbitrates between the workers, so there is no state to
// read and no state to judge: a worker which dies releases its lock, and
// the next worker takes it at once.
//
// A directory could not do this. Deciding that a gate is stale means
// reading it, and the gate may be replaced between that reading and the
// removal of it: the worker which removes it then removes the fresh gate of
// another worker, and both enter the critical section.
func (f *File) gate(key Key) (func(), error) {
	path := f.claimPath(key) + ".lock"
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(gateTimeout)
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() {
				// Closing the file releases the lock, whether or not the
				// unlock succeeds.
				_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
				file.Close()
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			file.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			file.Close()
			return nil, fmt.Errorf("the claim on %s is busy", key.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// claimOnDisk is the claim of a step as it is stored in a file.
type claimOnDisk struct {
	owner string
	until time.Time
	// hasExpiry tells whether the holder wrote the moment its claim
	// expires. A claim without one is judged by the age of its file.
	hasExpiry bool
}

// expired reports whether the claim may be taken over.
func (c claimOnDisk) expired(path string, lease time.Duration) bool {
	if c.hasExpiry {
		return time.Now().After(c.until)
	}
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	return time.Since(info.ModTime()) > lease
}

// readClaim reads the claim of a step from its file.
func readClaim(path string) (claimOnDisk, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return claimOnDisk{}, false, nil
		}
		return claimOnDisk{}, false, err
	}
	lines := strings.SplitN(string(data), "\n", 2)
	claim := claimOnDisk{owner: lines[0]}
	if len(lines) == 2 {
		until, err := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
		if err != nil {
			return claimOnDisk{}, false, fmt.Errorf("read the claim in %s: %w", path, err)
		}
		claim.until = time.Unix(0, until)
		claim.hasExpiry = true
	}
	return claim, true, nil
}

func (f *File) Store(_ context.Context, key Key, _ string, result map[string]any) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	target := f.resultPath(key)
	temp, err := os.CreateTemp(f.dir, f.name(key)+".tmp-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, target); err != nil {
		return err
	}
	return nil
}
