package api

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// MaxNameLen is the longest a sandbox name may be.
const MaxNameLen = 40

// ValidName reports why name can't be a sandbox's name, or nil if it can:
// 1 to 40 lowercase letters, digits and hyphens, starting and ending with
// a letter or digit. Names never contain "_", so one can't be mistaken
// for an id.
func ValidName(name string) error {
	if name == "" {
		return fmt.Errorf("a sandbox name can't be empty")
	}
	if len(name) > MaxNameLen {
		return fmt.Errorf("sandbox name %q is too long: at most %d characters", name, MaxNameLen)
	}
	for i, c := range name {
		switch {
		case 'a' <= c && c <= 'z', '0' <= c && c <= '9':
		case c == '-' && i > 0 && i < len(name)-1:
		default:
			return fmt.Errorf("sandbox name %q won't do: use lowercase letters, digits and hyphens, like api-tests, starting and ending with a letter or digit", name)
		}
	}
	return nil
}

// Words for names, desert-themed: an adjective, a landform and two digits,
// like amber-dune-42.
var (
	nameAdjectives = []string{
		"amber", "ashen", "bright", "calm", "copper", "dry", "dusky", "dusty",
		"faded", "gilded", "golden", "hazy", "honey", "hushed", "idle", "lucky",
		"keen", "lone", "low", "mellow", "misty", "ochre", "pale", "quiet",
		"rosy", "rusty", "sandy", "silent", "slow", "still", "sunny", "swift",
		"tan", "tawny", "warm", "wild", "windy", "young",
	}
	nameNouns = []string{
		"arroyo", "basin", "bluff", "butte", "canyon", "cactus", "cairn", "cliff",
		"crest", "dune", "dust", "erg", "flat", "gully", "hollow", "mesa",
		"mirage", "oasis", "pass", "playa", "ridge", "rock", "sage", "sand",
		"scrub", "shale", "slope", "spring", "stone", "wadi", "wash", "yucca",
	}
)

// NewName returns a random sandbox name such as "amber-dune-42".
func NewName() string {
	return pick(nameAdjectives) + "-" + pick(nameNouns) + fmt.Sprintf("-%02d", randInt(100))
}

func pick(words []string) string { return words[randInt(len(words))] }

func randInt(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic(err)
	}
	return int(v.Int64())
}
