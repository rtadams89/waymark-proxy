package dbbolt

import (
	"encoding/json"
	"errors"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

// MaxValueSize is the largest serialized value accepted for one key.
const MaxValueSize = 64 * 1024 * 1024

type Database struct {
	Db interface{}
}

func NewBoltDatabase(dbfile string) (*Database, error) {
	db, err := bolt.Open(dbfile, 0600, nil)
	if err != nil {
		return nil, err
	}

	return &Database{
		Db: db,
	}, err
}

func (d *Database) NewTable(tableName string) error {
	err := d.Db.(*bolt.DB).Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(tableName))
		if err != nil {
			return err
		}
		return nil
	})

	return err
}

func (d *Database) TableExists(tableName string) bool {
	return d.Db.(*bolt.DB).View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(tableName))
		if b == nil {
			return errors.New("table not exists")
		}
		return nil
	}) == nil
}

func (d *Database) DropTable(tableName string) error {
	err := d.Db.(*bolt.DB).Update(func(tx *bolt.Tx) error {
		err := tx.DeleteBucket([]byte(tableName))
		if err != nil {
			return err
		}
		return nil
	})
	return err
}

func (d *Database) Write(tableName string, key string, value interface{}) error {
	jsonString, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(jsonString) > MaxValueSize {
		return fmt.Errorf("value too large for key %s in table %s: %d bytes exceeds the limit of %d bytes", key, tableName, len(jsonString), MaxValueSize)
	}
	err = d.Db.(*bolt.DB).Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(tableName))
		if err != nil {
			return err
		}
		b := tx.Bucket([]byte(tableName))
		err = b.Put([]byte(key), jsonString)
		return err
	})
	return err
}

func (d *Database) Read(tableName string, key string, assignee interface{}) error {
	err := d.Db.(*bolt.DB).View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(tableName))
		v := b.Get([]byte(key))
		json.Unmarshal(v, &assignee)
		return nil
	})
	return err
}

func (d *Database) KeyExists(tableName string, key string) bool {
	resultIsNil := false
	if !d.TableExists(tableName) {
		return false
	}
	err := d.Db.(*bolt.DB).View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(tableName))
		v := b.Get([]byte(key))
		if v == nil {
			resultIsNil = true
		}
		return nil
	})

	if err != nil {
		return false
	} else {
		if resultIsNil {
			return false
		} else {
			return true
		}
	}
}

func (d *Database) Delete(tableName string, key string) error {
	err := d.Db.(*bolt.DB).Update(func(tx *bolt.Tx) error {
		tx.Bucket([]byte(tableName)).Delete([]byte(key))
		return nil
	})

	return err
}

func (d *Database) ListTable(tableName string) ([][][]byte, error) {
	var results [][][]byte
	err := d.Db.(*bolt.DB).View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(tableName))
		c := b.Cursor()

		for k, v := c.First(); k != nil; k, v = c.Next() {
			results = append(results, [][]byte{k, v})
		}
		return nil
	})
	return results, err
}

func (d *Database) Close() {
	d.Db.(*bolt.DB).Close()
}
