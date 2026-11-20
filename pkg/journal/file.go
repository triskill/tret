package journal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// gateTimeout bounds how long a worker waits for the gate which
	// serializes the takeover of an expired claim.
	gateTimeout = 30 * time.Second
)

// File keeps the results of the steps in a directory, so they survive the
// restart of a worker and are shared by the workers of one node.
//
// A claim is a file which only its owner may create: the first process to
// create it holds the step. A claim whose expiry has passed is taken over,
// which is what happens when the worker holding it died. The takeover is
// serialized by a gate, so two workers cannot take the same claim over. The
// result is written to a temporary file and renamed, so a reader never sees
// half of it.
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

func (f *File) Acquire(_ context.Context, key Key, owner string, lease time.Duration) (bool, error) {
	path := f.claimPath(key)
	// Nobody holds the claim: take it. The exclusive create decides between
	// the workers which try at the same time.
	if claimed, err := f.createClaim(path, owner, lease); claimed || err != nil {
		return claimed, err
	}
	// Somebody holds it. Is the claim expired?
	if !claimExpired(path, lease) {
		return false, nil
	}
	// The takeover of an expired claim is serialized. Without it two
	// contenders could both see the expired claim, and the second one would
	// remove the claim the first one has just taken — and then both would
	// run the work.
	release, err := f.gate(key)
	if err != nil {
		return false, err
	}
	defer release()
	// The claim may have been replaced while we waited for the gate.
	if !claimExpired(path, lease) {
		return false, nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	return f.createClaim(path, owner, lease)
}

// createClaim creates the claim file, which only one worker may do.
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
	claim := owner + "\n" + strconv.FormatInt(time.Now().Add(lease).UnixNano(), 10)
	if _, err := file.WriteString(claim); err != nil {
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

// gate serializes the workers which take an expired claim over. The gate is
// a directory, which only one worker may create.
func (f *File) gate(key Key) (func(), error) {
	path := f.claimPath(key) + ".gate"
	deadline := time.Now().Add(gateTimeout)
	for {
		err := os.Mkdir(path, 0700)
		if err == nil {
			return func() { os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		// The gate is held by a worker which may have died.
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > gateTimeout {
			os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the claim on %s is busy", key.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// claimExpired reports whether the claim may be taken over. The expiry the
// holder wrote decides; the age of the file decides when the claim does not
// carry one.
func claimExpired(path string, lease time.Duration) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		// The claim is gone: it may be taken over.
		return true
	}
	lines := strings.SplitN(string(data), "\n", 2)
	if len(lines) == 2 {
		until, err := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
		if err == nil {
			return time.Now().After(time.Unix(0, until))
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	return time.Since(info.ModTime()) > lease
}

// Claim returns the claim on the step.
func (f *File) Claim(_ context.Context, key Key) (ClaimInfo, bool, error) {
	data, err := os.ReadFile(f.claimPath(key))
	if err != nil {
		if os.IsNotExist(err) {
			return ClaimInfo{}, false, nil
		}
		return ClaimInfo{}, false, err
	}
	lines := strings.SplitN(string(data), "\n", 2)
	info := ClaimInfo{Owner: lines[0]}
	if len(lines) == 2 {
		until, err := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
		if err != nil {
			return ClaimInfo{}, false, fmt.Errorf("read the claim on %s: %w", key.String(), err)
		}
		info.Until = time.Unix(0, until)
	}
	return info, true, nil
}

func (f *File) Release(_ context.Context, key Key, owner string) error {
	path := f.claimPath(key)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if strings.SplitN(string(data), "\n", 2)[0] != owner {
		// Another worker holds the claim now; its file must stay.
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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
