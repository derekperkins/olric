// Copyright 2018-2022 Burak Sezer
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/*Package kvstore implements a GC friendly in-memory storage engine by using
built-in maps and byte slices. It also supports compaction.*/
package kvstore

import (
	"errors"
	"fmt"
	"log"
	"reflect"
	"time"

	"github.com/buraksezer/olric/internal/kvstore/entry"
	"github.com/buraksezer/olric/internal/kvstore/table"
	"github.com/buraksezer/olric/pkg/storage"
)

const (
	maxGarbageRatio = 0.40
	// 1MB
	defaultTableSize = uint64(1 << 20)

	defaultMaxIdleTableTimeout = 15 * time.Minute
)

// KVStore implements an in-memory storage engine.
type KVStore struct {
	coefficient         uint64
	tablesByCoefficient map[uint64]*table.Table
	tables              []*table.Table
	config              *storage.Config
}

func DefaultConfig() *storage.Config {
	options := storage.NewConfig(nil)
	options.Add("tableSize", defaultTableSize)
	options.Add("maxIdleTableTimeout", defaultMaxIdleTableTimeout)
	return options
}

func New(c *storage.Config) *KVStore {
	return &KVStore{
		tablesByCoefficient: make(map[uint64]*table.Table),
		config:              c,
	}
}

func (k *KVStore) SetConfig(c *storage.Config) {
	k.config = c
}

func (k *KVStore) makeTable() error {
	if len(k.tables) != 0 {
		head := k.tables[len(k.tables)-1]
		head.SetState(table.ReadOnlyState)

		for i, t := range k.tables {
			if t.State() == table.RecycledState {
				k.tables = append(k.tables, t)
				k.tables = append(k.tables[:i], k.tables[i+1:]...)
				t.SetState(table.ReadWriteState)
				return nil
			}
		}
	}

	tmpSize, err := k.config.Get("tableSize")
	if err != nil {
		return err
	}

	size, err := prepareTableSize(tmpSize)
	if err != nil {
		return err
	}

	current := table.New(size)
	k.tables = append(k.tables, current)
	k.tablesByCoefficient[k.coefficient] = current
	k.coefficient++
	return nil
}

func (k *KVStore) SetLogger(_ *log.Logger) {}

func (k *KVStore) Start() error {
	if k.config == nil {
		return errors.New("config cannot be nil")
	}
	return nil
}

func prepareTableSize(raw interface{}) (size uint64, err error) {
	switch raw.(type) {
	case uint:
		size = uint64(raw.(uint))
	case uint8:
		size = uint64(raw.(uint8))
	case uint16:
		size = uint64(raw.(uint16))
	case uint32:
		size = uint64(raw.(uint32))
	case uint64:
		size = raw.(uint64)
	case int:
		size = uint64(raw.(int))
	case int8:
		size = uint64(raw.(int8))
	case int16:
		size = uint64(raw.(int16))
	case int32:
		size = uint64(raw.(int32))
	case int64:
		size = uint64(raw.(int64))
	default:
		err = fmt.Errorf("invalid type for tableSize: %s", reflect.TypeOf(raw))
		return
	}
	return
}

// Fork creates a new KVStore instance.
func (k *KVStore) Fork(c *storage.Config) (storage.Engine, error) {
	if c == nil {
		c = k.config.Copy()
	}
	tmpSize, err := c.Get("tableSize")
	if err != nil {
		return nil, err
	}

	size, err := prepareTableSize(tmpSize)
	if err != nil {
		return nil, err
	}

	child := New(c)
	t := table.New(size)
	child.tables = append(child.tables, t)
	child.tablesByCoefficient[k.coefficient] = t
	child.coefficient++
	return child, nil
}

func (k *KVStore) AppendTable(t *table.Table) {
	k.tables = append(k.tables, t)
	k.tablesByCoefficient[k.coefficient] = t
	k.coefficient++
}

func (k *KVStore) Name() string {
	return "kvstore"
}

func (k *KVStore) NewEntry() storage.Entry {
	return entry.New()
}

// PutRaw sets the raw value for the given key.
func (k *KVStore) PutRaw(hkey uint64, value []byte) error {
	if len(k.tables) == 0 {
		if err := k.makeTable(); err != nil {
			return err
		}
	}

	for {
		// Get the last value, storage only calls Put on the last created table.
		t := k.tables[len(k.tables)-1]
		err := t.PutRaw(hkey, value)
		if errors.Is(err, table.ErrNotEnoughSpace) {
			err := k.makeTable()
			if err != nil {
				return err
			}
			// try again
			continue
		}
		if err != nil {
			return err
		}
		// everything is ok
		break
	}

	return nil
}

// Put sets the value for the given key. It overwrites any previous value for that key
func (k *KVStore) Put(hkey uint64, value storage.Entry) error {
	if len(k.tables) == 0 {
		if err := k.makeTable(); err != nil {
			return err
		}
	}

	for {
		// Get the last value, storage only calls Put on the last created table.
		t := k.tables[len(k.tables)-1]
		err := t.Put(hkey, value)
		if errors.Is(err, table.ErrNotEnoughSpace) {
			err := k.makeTable()
			if err != nil {
				return err
			}
			// try again
			continue
		}
		if err != nil {
			return err
		}

		// everything is ok
		break
	}

	return nil
}

// GetRaw extracts encoded value for the given hkey. This is useful for merging tables.
func (k *KVStore) GetRaw(hkey uint64) ([]byte, error) {
	// Scan available tables by starting the last added table.
	for i := len(k.tables) - 1; i >= 0; i-- {
		t := k.tables[i]
		raw, err := t.GetRaw(hkey)
		if errors.Is(err, table.ErrHKeyNotFound) {
			// Try out the other tables.
			continue
		}
		if err != nil {
			return nil, err
		}
		// Found the key, return the stored value with its metadata.
		return raw, nil
	}

	// Nothing here.
	return nil, storage.ErrKeyNotFound
}

// Get gets the value for the given key. It returns storage.ErrKeyNotFound if the DB
// does not contain the key. The returned Entry is its own copy,
// it is safe to modify the contents of the returned slice.
func (k *KVStore) Get(hkey uint64) (storage.Entry, error) {
	// Scan available tables by starting the last added table.
	for i := len(k.tables) - 1; i >= 0; i-- {
		t := k.tables[i]
		res, err := t.Get(hkey)
		if errors.Is(err, table.ErrHKeyNotFound) {
			// Try out the other tables.
			continue
		}
		if err != nil {
			return nil, err
		}
		// Found the key, return the stored value with its metadata.
		return res, nil
	}
	// Nothing here.
	return nil, storage.ErrKeyNotFound
}

// GetTTL gets the timeout for the given key. It returns storage.ErrKeyNotFound if the DB
// does not contain the key.
func (k *KVStore) GetTTL(hkey uint64) (int64, error) {
	// Scan available tables by starting the last added table.
	for i := len(k.tables) - 1; i >= 0; i-- {
		t := k.tables[i]
		ttl, err := t.GetTTL(hkey)
		if errors.Is(err, table.ErrHKeyNotFound) {
			// Try out the other tables.
			continue
		}
		if err != nil {
			return 0, err
		}
		// Found the key, return its ttl
		return ttl, nil
	}

	// Nothing here.
	return 0, storage.ErrKeyNotFound
}

func (k *KVStore) GetLastAccess(hkey uint64) (int64, error) {
	// Scan available tables by starting the last added table.
	for i := len(k.tables) - 1; i >= 0; i-- {
		t := k.tables[i]
		lastAccess, err := t.GetLastAccess(hkey)
		if errors.Is(err, table.ErrHKeyNotFound) {
			// Try out the other tables.
			continue
		}
		if err != nil {
			return 0, err
		}
		// Found the key, return its ttl
		return lastAccess, nil
	}

	// Nothing here.
	return 0, storage.ErrKeyNotFound
}

// GetKey gets the key for the given hkey. It returns storage.ErrKeyNotFound if the DB
// does not contain the key.
func (k *KVStore) GetKey(hkey uint64) (string, error) {
	// Scan available tables by starting the last added table.
	for i := len(k.tables) - 1; i >= 0; i-- {
		t := k.tables[i]
		key, err := t.GetKey(hkey)
		if errors.Is(err, table.ErrHKeyNotFound) {
			// Try out the other tables.
			continue
		}
		if err != nil {
			return "", err
		}
		// Found the key, return its ttl
		return key, nil
	}

	// Nothing here.
	return "", storage.ErrKeyNotFound
}

// Delete deletes the value for the given key. Delete will not returns error if key doesn't exist.
func (k *KVStore) Delete(hkey uint64) error {
	// Scan available tables by starting the last added table.
	for i := len(k.tables) - 1; i >= 0; i-- {
		t := k.tables[i]
		err := t.Delete(hkey)
		if errors.Is(err, table.ErrHKeyNotFound) {
			// Try out the other tables.
			continue
		}
		if err != nil {
			return err
		}
		break
	}

	return nil
}

// UpdateTTL updates the expiry for the given key.
func (k *KVStore) UpdateTTL(hkey uint64, data storage.Entry) error {
	// Scan available tables by starting the last added table.
	for i := len(k.tables) - 1; i >= 0; i-- {
		t := k.tables[i]
		err := t.UpdateTTL(hkey, data)
		if errors.Is(err, table.ErrHKeyNotFound) {
			// Try out the other tables.
			continue
		}
		if err != nil {
			return err
		}
		// Found the key, return the stored value with its metadata.
		return nil
	}
	// Nothing here.
	return storage.ErrKeyNotFound
}

// Stats is a function which provides memory allocation and garbage ratio of a storage instance.
func (k *KVStore) Stats() storage.Stats {
	stats := storage.Stats{
		NumTables: len(k.tables),
	}
	for _, t := range k.tables {
		s := t.Stats()
		stats.Allocated += int(s.Allocated)
		stats.Inuse += int(s.Inuse)
		stats.Garbage += int(s.Garbage)
		stats.Length += s.Length
	}
	return stats
}

// Check checks the key existence.
func (k *KVStore) Check(hkey uint64) bool {
	// Scan available tables by starting the last added table.
	for i := len(k.tables) - 1; i >= 0; i-- {
		t := k.tables[i]
		ok := t.Check(hkey)
		if ok {
			return true
		}
	}

	// Nothing there.
	return false
}

// Range calls f sequentially for each key and value present in the map.
// If f returns false, range stops the iteration. Range may be O(N) with
// the number of elements in the map even if f returns false after a constant
// number of calls.
func (k *KVStore) Range(f func(hkey uint64, e storage.Entry) bool) {
	// Scan available tables by starting the last added table.
	for i := len(k.tables) - 1; i >= 0; i-- {
		t := k.tables[i]
		t.Range(func(hkey uint64, e storage.Entry) bool {
			return f(hkey, e)
		})
	}
}

// RangeHKey calls f sequentially for each key present in the map.
// If f returns false, range stops the iteration. Range may be O(N) with
// the number of elements in the map even if f returns false after a constant
// number of calls.
func (k *KVStore) RangeHKey(f func(hkey uint64) bool) {
	// Scan available tables by starting the last added table.
	for i := len(k.tables) - 1; i >= 0; i-- {
		t := k.tables[i]
		t.RangeHKey(func(hkey uint64) bool {
			return f(hkey)
		})
	}
}

func (k *KVStore) Scan(cursor uint64, count int, f func(e storage.Entry) bool) (uint64, error) {
	tmp, err := k.config.Get("tableSize")
	if err != nil {
		return 0, err
	}
	size := tmp.(uint64)

	cf := cursor / size
	t := k.tablesByCoefficient[cf]
	if cf > 0 {
		cursor = cursor - (size * cf)
	}

	cursor, err = t.Scan(cursor, count, f)
	if err != nil {
		return 0, err
	}

	if cursor == 0 {
		_, ok := k.tablesByCoefficient[cf+1]
		if !ok {
			return 0, nil
		}
		return size * (cf + 1), nil
	}

	return cursor + (size * cf), nil
}

func (k *KVStore) ScanRegexMatch(cursor uint64, expr string, count int, f func(e storage.Entry) bool) (uint64, error) {
	raw, err := k.config.Get("tableSize")
	if err != nil {
		return 0, err
	}
	size, err := prepareTableSize(raw)
	if err != nil {
		return 0, err
	}

	cf := cursor / size
	t := k.tablesByCoefficient[cf]
	if cf > 0 {
		cursor = cursor - (size * cf)
	}

	cursor, err = t.ScanRegexMatch(cursor, expr, count, f)
	if err != nil {
		return 0, err
	}

	if cursor == 0 {
		_, ok := k.tablesByCoefficient[cf+1]
		if !ok {
			return 0, nil
		}
		return size * (cf + 1), nil
	}

	return cursor + (size * cf), nil

}

func (k *KVStore) Close() error {
	return nil
}

func (k *KVStore) Destroy() error {
	return nil
}

var _ storage.Engine = (*KVStore)(nil)
