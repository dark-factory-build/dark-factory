// Package relayhost is the factoryd side of the remote-access relay. It owns
// the node key that names one factory to the relay, the credentials minted
// from it, the binary record envelope, and the outbound connector that pipes
// relay-opened controller sessions into the daemon's own loopback browser
// listener. The daemon cannot distinguish a relayed session from a local one;
// capabilities in the daemon-owned grant remain the only authority.
package relayhost

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/install"
)

const (
	nodeKeyFileName = "node.key"
	maxIdentityFile = 1 << 12
)

// ErrIdentity is every refusal to load or create the node identity. The
// concrete cause is joined for operators; callers only branch on this.
var ErrIdentity = errors.New("relayhost: node identity unavailable")

// Identity is one factory's relay identity plus the boot generation observed
// by the LoadOrCreate call that produced it. It is immutable and safe to copy.
type Identity struct {
	private    ed25519.PrivateKey
	nodeID     string
	generation uint64
}

// LoadOrCreate opens (or creates) the node key under <home>/relay. The boot
// generation the relay orders hosts by is simply the wall clock in seconds,
// so nothing about it has to survive a restart: a home restored from backup or
// reinstalled with the same key still presents a newer boot than the one the
// relay last accepted.
//
// The cost is that a clock stepping backwards past the last accepted boot
// locks this node out of the relay until the clock passes that instant again.
// The escape hatch is a fresh node key: a new key is a new node id, which the
// relay routes to a Durable Object with no accepted host at all.
func LoadOrCreate(home string) (Identity, error) { return loadOrCreateAt(home, time.Now()) }

func loadOrCreateAt(home string, now time.Time) (Identity, error) {
	if home == "" || !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return Identity{}, fmt.Errorf("%w: home must be an absolute clean path", ErrIdentity)
	}
	directory := filepath.Join(home, install.RelayDirectoryName)
	if err := prepareDirectory(directory); err != nil {
		return Identity{}, err
	}
	seed, err := loadOrCreateSeed(filepath.Join(directory, nodeKeyFileName))
	if err != nil {
		return Identity{}, err
	}
	generation := now.Unix()
	if generation <= 0 {
		return Identity{}, fmt.Errorf("%w: the clock is before the epoch", ErrIdentity)
	}
	private := ed25519.NewKeyFromSeed(seed)
	return Identity{private: private, nodeID: NodeIDFromPublicKey(private.Public().(ed25519.PublicKey)), generation: uint64(generation)}, nil
}

// NodeID is the relay's Durable Object name for this factory.
func (identity Identity) NodeID() string { return identity.nodeID }

// PublicKey is the node public key the relay checks the node id against.
func (identity Identity) PublicKey() ed25519.PublicKey {
	if identity.private == nil {
		return nil
	}
	return identity.private.Public().(ed25519.PublicKey)
}

// Generation is the daemon start time in unix seconds that LoadOrCreate
// observed. It is read by tests and by factoryctl remote status once the
// pairing slice lands.
func (identity Identity) Generation() uint64 { return identity.generation }

func (identity Identity) valid() bool {
	return len(identity.private) == ed25519.PrivateKeySize && identity.nodeID != "" && identity.generation != 0
}

// NodeIDFromPublicKey derives the self-certifying node id: lowercase RFC 4648
// base32 without padding of the first 20 bytes of SHA-256 of the 32-byte
// public key, exactly 32 characters.
func NodeIDFromPublicKey(key ed25519.PublicKey) string {
	if len(key) != ed25519.PublicKeySize {
		return ""
	}
	digest := sha256.Sum256(key)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(digest[:20]))
}

func prepareDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return errors.Join(fmt.Errorf("%w: create %s", ErrIdentity, directory), err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return errors.Join(fmt.Errorf("%w: inspect %s", ErrIdentity, directory), err)
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is not a directory", ErrIdentity, directory)
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(directory, 0o700); err != nil {
			return errors.Join(fmt.Errorf("%w: restrict %s", ErrIdentity, directory), err)
		}
	}
	return nil
}

func loadOrCreateSeed(path string) ([]byte, error) {
	text, found, err := readPrivateFile(path)
	if err != nil {
		return nil, err
	}
	if found {
		seed, err := hex.DecodeString(strings.TrimSpace(text))
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("%w: %s is not a %d byte hex seed", ErrIdentity, path, ed25519.SeedSize)
		}
		return seed, nil
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, errors.Join(fmt.Errorf("%w: generate node key", ErrIdentity), err)
	}
	if err := writeDurable(path, []byte(hex.EncodeToString(seed)+"\n")); err != nil {
		return nil, err
	}
	return seed, nil
}

// readPrivateFile refuses a symlink, a non-regular file, or any file another
// account can read. Those are the shapes that would let a second identity be
// substituted for this factory's own.
func readPrivateFile(path string) (string, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, errors.Join(fmt.Errorf("%w: inspect %s", ErrIdentity, path), err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("%w: %s is not a regular file", ErrIdentity, path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", false, fmt.Errorf("%w: %s is readable beyond its owner", ErrIdentity, path)
	}
	if info.Size() > maxIdentityFile {
		return "", false, fmt.Errorf("%w: %s is too large", ErrIdentity, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, errors.Join(fmt.Errorf("%w: read %s", ErrIdentity, path), err)
	}
	return string(data), true, nil
}

func writeDurable(path string, data []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, filepath.Base(path)+".tmp-*")
	if err != nil {
		return errors.Join(fmt.Errorf("%w: stage %s", ErrIdentity, path), err)
	}
	name := temporary.Name()
	commit := func() error {
		if _, err := temporary.Write(data); err != nil {
			return err
		}
		if err := temporary.Chmod(0o600); err != nil {
			return err
		}
		if err := temporary.Sync(); err != nil {
			return err
		}
		return temporary.Close()
	}
	if err := commit(); err != nil {
		_ = temporary.Close()
		_ = os.Remove(name)
		return errors.Join(fmt.Errorf("%w: write %s", ErrIdentity, path), err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return errors.Join(fmt.Errorf("%w: commit %s", ErrIdentity, path), err)
	}
	handle, err := os.Open(directory)
	if err != nil {
		return errors.Join(fmt.Errorf("%w: open %s", ErrIdentity, directory), err)
	}
	syncErr := handle.Sync()
	closeErr := handle.Close()
	if syncErr != nil || closeErr != nil {
		return errors.Join(fmt.Errorf("%w: sync %s", ErrIdentity, directory), syncErr, closeErr)
	}
	return nil
}
