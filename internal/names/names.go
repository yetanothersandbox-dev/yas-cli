// Package names generates the caller-chosen sandbox ids the create API
// requires. adjective-noun-4hex: readable in a picker, unique enough that two
// laptops naming boxes on the same tenant will not collide in practice — and
// if they do, the create is idempotent on the id, so the second caller finds
// out honestly.
package names

import (
	"crypto/rand"
	"encoding/hex"
	"math/big"
)

var adjectives = []string{
	"amber", "bold", "brisk", "calm", "clear", "deft", "eager", "fleet",
	"glad", "keen", "lucid", "mellow", "nimble", "quiet", "rapid", "sharp",
	"solid", "swift", "tidy", "vivid",
}

var nouns = []string{
	"badger", "condor", "cricket", "falcon", "gecko", "heron", "ibex",
	"jackal", "lemur", "linnet", "marten", "otter", "petrel", "plover",
	"quokka", "raven", "shrike", "stoat", "tern", "wren",
}

func pick(list []string) string {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(list))))
	if err != nil {
		return list[0]
	}
	return list[n.Int64()]
}

// Generate returns e.g. "brisk-otter-4f2a".
func Generate() string {
	suffix := make([]byte, 2)
	if _, err := rand.Read(suffix); err != nil {
		suffix = []byte{0, 0}
	}
	return pick(adjectives) + "-" + pick(nouns) + "-" + hex.EncodeToString(suffix)
}
