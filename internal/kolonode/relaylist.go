package kolonode

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// envRelays is the environment variable read for relays when neither -relay nor -relay-file is set.
const envRelays = "KOLO_RELAYS"

// relayEntry is a relay to check: its address and its kolo-id key.
type relayEntry struct {
	addr entry
	key  ed25519.PublicKey
}

func (r relayEntry) String() string { return r.addr.String() + "@" + hex.EncodeToString(r.key) }

// parseRelayEntry parses host:port@key, the key as 64 hex characters, with the rules of kolo-swarm's ParseRelays.
func parseRelayEntry(s string) (relayEntry, error) {
	addr, k, ok := strings.Cut(s, "@")
	if !ok {
		return relayEntry{}, errors.New("want host:port@key")
	}
	e, err := parseEntry(addr)
	if err != nil {
		return relayEntry{}, err
	}
	key, err := hex.DecodeString(k)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return relayEntry{}, fmt.Errorf("the key must be %d hex characters", 2*ed25519.PublicKeySize)
	}
	return relayEntry{addr: e, key: key}, nil
}

// relayList gathers relays without duplicates; one key at two addresses or one address with two keys is an error,
// since one relay process has one key.
type relayList struct {
	relays []relayEntry
}

// add parses s and adds it; it reports whether the list is now over maxEntries.
func (l *relayList) add(s string) (bool, error) {
	r, err := parseRelayEntry(s)
	if err != nil {
		return false, err
	}
	for _, o := range l.relays {
		sameKey, sameAddr := bytes.Equal(o.key, r.key), o.addr.String() == r.addr.String()
		switch {
		case sameKey && sameAddr:
			return false, nil
		case sameKey:
			return false, errors.New("the key of an earlier relay with another address")
		case sameAddr:
			return false, errors.New("the address of an earlier relay with another key")
		}
	}
	l.relays = append(l.relays, r)
	return len(l.relays) > maxEntries, nil
}

// loadRelays returns the -relay entries, then each -relay-file's in order, or else KOLO_RELAYS. A flag set to an
// empty list never falls back to KOLO_RELAYS.
func loadRelays(flags, files []string, set bool) ([]relayEntry, error) {
	var l relayList
	tooMany := fmt.Errorf("kolonode: more than %d relays in all", maxEntries)
	for i, s := range flags {
		if over, err := l.add(s); err != nil {
			return nil, fmt.Errorf("kolonode: -relay entry %d: %w", i+1, err)
		} else if over {
			return nil, tooMany
		}
	}
	for _, p := range files {
		b, err := readFile(p, "-relay-file")
		if err != nil {
			return nil, err
		}
		err = fileEntries(b, func(line int, s string) error {
			if over, err := l.add(s); err != nil {
				return fmt.Errorf("kolonode: -relay-file %s line %d: %w", p, line, err)
			} else if over {
				return tooMany
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if !set {
		for i, s := range splitList(os.Getenv(envRelays)) {
			if over, err := l.add(s); err != nil {
				return nil, fmt.Errorf("kolonode: %s entry %d: %w", envRelays, i+1, err)
			} else if over {
				return nil, tooMany
			}
		}
	}
	return l.relays, nil
}
