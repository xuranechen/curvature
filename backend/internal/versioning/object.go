package versioning

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	// errCorruptObject marks a damaged or tampered object. Callers surface it
	// instead of silently returning wrong content.
	errCorruptObject = errors.New("versioning: corrupt object")
	// errWrongKind marks a hash that resolves to an unexpected object kind.
	errWrongKind = errors.New("versioning: unexpected object kind")
)

// hashFramed returns the sha256 content address of a framed object.
func hashFramed(framed []byte) string {
	sum := sha256.Sum256(framed)
	return hex.EncodeToString(sum[:])
}

// frameObject builds the git-like "<kind> <size>\x00<payload>" envelope. The
// declared size lets a reader reject truncated payloads.
func frameObject(kind string, payload []byte) []byte {
	header := kind + " " + strconv.Itoa(len(payload)) + "\x00"
	framed := make([]byte, 0, len(header)+len(payload))
	framed = append(framed, header...)
	framed = append(framed, payload...)
	return framed
}

// unframeObject splits a framed object and validates its declared size.
func unframeObject(framed []byte) (string, []byte, error) {
	idx := bytes.IndexByte(framed, 0)
	if idx < 0 {
		return "", nil, errCorruptObject
	}
	header := string(framed[:idx])
	space := strings.IndexByte(header, ' ')
	if space <= 0 || space == len(header)-1 {
		return "", nil, errCorruptObject
	}
	kind := header[:space]
	size, err := strconv.Atoi(header[space+1:])
	if err != nil || size < 0 {
		return "", nil, errCorruptObject
	}
	payload := framed[idx+1:]
	if len(payload) != size {
		return "", nil, fmt.Errorf("%w: declared size %d, actual %d", errCorruptObject, size, len(payload))
	}
	return kind, payload, nil
}

// objectRelPath maps a sha256 hash to its sharded relative path.
func objectRelPath(hash string) string {
	return filepath.Join("objects", hash[:2], hash[2:])
}

// writeObject stores a payload under its content address and returns the hash.
// It is idempotent: an object that already exists is left untouched, which is
// what makes repeated snapshots cheap.
// encodeObject frames and compresses an object, returning its content address.
//
// Separated from the write so a caller scanning in parallel can hash and
// compress concurrently and serialize only the store step.
func encodeObject(kind string, payload []byte) (string, []byte, error) {
	framed := frameObject(kind, payload)
	hash := hashFramed(framed)
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(framed); err != nil {
		return "", nil, err
	}
	if err := zw.Close(); err != nil {
		return "", nil, err
	}
	return hash, buf.Bytes(), nil
}

// storeEncoded writes an already-encoded object unless it is present.
//
// The existence check and the write are one step on purpose: two writers racing
// on the same content address would otherwise both miss the check and both
// rename onto the same path, which on Windows fails with a sharing violation.
// Callers that write concurrently must serialize this call.
func storeEncoded(storeDir, hash string, encoded []byte) error {
	dest := filepath.Join(storeDir, objectRelPath(hash))
	if _, err := os.Stat(dest); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return atomicWriteFile(dest, encoded, 0o644)
}

func writeObject(storeDir, kind string, payload []byte) (string, error) {
	hash, encoded, err := encodeObject(kind, payload)
	if err != nil {
		return "", err
	}
	if err := storeEncoded(storeDir, hash, encoded); err != nil {
		return "", err
	}
	return hash, nil
}

// readObject loads an object and verifies its content address. A mismatch or a
// malformed envelope is reported as errCorruptObject rather than ignored.
func readObject(storeDir, hash string) (string, []byte, error) {
	if !validHash(hash) {
		return "", nil, fmt.Errorf("%w: malformed hash %q", errCorruptObject, hash)
	}
	raw, err := os.ReadFile(filepath.Join(storeDir, objectRelPath(hash)))
	if err != nil {
		return "", nil, err
	}
	zr, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", errCorruptObject, err)
	}
	defer func() { _ = zr.Close() }()

	framed, err := io.ReadAll(zr)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", errCorruptObject, err)
	}
	kind, payload, err := unframeObject(framed)
	if err != nil {
		return "", nil, err
	}
	if got := hashFramed(framed); got != hash {
		return "", nil, fmt.Errorf("%w: address mismatch (want %s, got %s)", errCorruptObject, hash, got)
	}
	return kind, payload, nil
}

// hasObject reports whether an object exists locally.
func hasObject(storeDir, hash string) bool {
	if !validHash(hash) {
		return false
	}
	_, err := os.Stat(filepath.Join(storeDir, objectRelPath(hash)))
	return err == nil
}

// objectBytes returns the compressed size on disk of an object.
func objectBytes(storeDir, hash string) int64 {
	info, err := os.Stat(filepath.Join(storeDir, objectRelPath(hash)))
	if err != nil {
		return 0
	}
	return info.Size()
}

// writeBlob stores raw file content and returns its content address.
func writeBlob(storeDir string, data []byte) (string, error) {
	return writeObject(storeDir, ObjectKindBlob, data)
}

// readBlob returns the raw bytes of a blob, verifying its address.
func readBlob(storeDir, hash string) ([]byte, error) {
	kind, payload, err := readObject(storeDir, hash)
	if err != nil {
		return nil, err
	}
	if kind != ObjectKindBlob {
		return nil, fmt.Errorf("%w: want %s, got %s", errWrongKind, ObjectKindBlob, kind)
	}
	return payload, nil
}

// writeManifest stores a manifest and returns its content address.
//
// encoding/json sorts map keys, so the payload — and therefore the address —
// is stable for a given set of entries.
func writeManifest(storeDir string, m Manifest) (string, error) {
	if m.Entries == nil {
		m.Entries = map[string]ManifestEntry{}
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return writeObject(storeDir, ObjectKindTree, payload)
}

// readManifest loads a manifest by content address.
func readManifest(storeDir, hash string) (Manifest, error) {
	kind, payload, err := readObject(storeDir, hash)
	if err != nil {
		return Manifest{}, err
	}
	if kind != ObjectKindTree {
		return Manifest{}, fmt.Errorf("%w: want %s, got %s", errWrongKind, ObjectKindTree, kind)
	}
	var m Manifest
	if err := json.Unmarshal(payload, &m); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", errCorruptObject, err)
	}
	if m.Entries == nil {
		m.Entries = map[string]ManifestEntry{}
	}
	return m, nil
}

// writeCommit stores the persisted commit payload and returns its address.
func writeCommit(storeDir string, doc commitDoc) (string, error) {
	payload, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return writeObject(storeDir, ObjectKindCommit, payload)
}

// readCommit loads a commit and re-attaches its derived Hash.
func readCommit(storeDir, hash string) (Commit, error) {
	kind, payload, err := readObject(storeDir, hash)
	if err != nil {
		return Commit{}, err
	}
	if kind != ObjectKindCommit {
		return Commit{}, fmt.Errorf("%w: want %s, got %s", errWrongKind, ObjectKindCommit, kind)
	}
	var doc commitDoc
	if err := json.Unmarshal(payload, &doc); err != nil {
		return Commit{}, fmt.Errorf("%w: %v", errCorruptObject, err)
	}
	return Commit{
		Hash:      hash,
		Parents:   doc.Parents,
		Manifest:  doc.Manifest,
		Message:   doc.Message,
		Author:    doc.Author,
		CreatedAt: doc.CreatedAt,
		Branch:    doc.Branch,
		Origin:    doc.Origin,
		Stats:     doc.Stats,
	}, nil
}
