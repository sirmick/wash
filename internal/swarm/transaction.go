package swarm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// Receipt makes a retried logical operation return the original outcome. It is
// stored in the same atomic snapshot as the mutation, including workspace setup.
type Receipt struct {
	Session   string          `json:"session"`
	Operation string          `json:"operation"`
	Request   string          `json:"request"`
	Digest    string          `json:"digest"`
	Result    json.RawMessage `json:"result"`
}

// Transaction stages store-only operations on a private copy, then persists once.
// Callbacks must never launch processes, notify users or perform external I/O.
func (s *Store) Transaction(session, operation, request string, raw []byte, preview bool, fn func(*Store) (any, error)) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(request) > 160 {
		return nil, errors.New("request_id exceeds 160 bytes")
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(normalized)
	hash := sha256.Sum256(encoded)
	digest := hex.EncodeToString(hash[:])
	if request != "" && !preview {
		for _, r := range s.state.Receipts {
			if r.Session == session && r.Operation == operation && r.Request == request {
				if r.Digest != digest {
					return nil, errors.New("request_id reused with different arguments")
				}
				return append(json.RawMessage(nil), r.Result...), nil
			}
		}
		if len(s.state.Receipts) >= 10000 {
			return nil, errors.New("workspace request receipt limit reached")
		}
	}
	staged := &Store{state: clone(s.state), write: func(string, []byte) error { return nil }}
	result, err := fn(staged)
	if err != nil {
		return nil, err
	}
	if preview {
		return result, nil
	}
	if request != "" {
		data, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		staged.state.Receipts = append(staged.state.Receipts, Receipt{session, operation, request, digest, data})
	}
	data, err := json.Marshal(staged.state)
	if err != nil {
		return nil, err
	}
	if err = s.write(s.path, data); err != nil {
		return nil, err
	}
	s.state = staged.state
	return result, nil
}
