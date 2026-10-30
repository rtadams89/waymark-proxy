package database

import (
	"log"
	"runtime"

	"imuslab.com/zoraxy/mod/database/dbinc"
)

type Database struct {
	Db          interface{} // nil on OpenWrt, leveldb.DB or bolt.DB otherwise
	BackendType dbinc.BackendType
	Backend     dbinc.Backend
}

func NewDatabase(dbfile string, backendType dbinc.BackendType) (*Database, error) {
	if runtime.GOARCH == "riscv64" {
		log.Println("RISCV hardware detected, ignoring the backend type and using FS emulated database")
	}
	return newDatabase(dbfile, backendType)
}

func GetRecommendedBackendType() dbinc.BackendType {
	if runtime.GOARCH == "riscv64" {
		return dbinc.BackendFSOnly
	} else if runtime.GOOS == "windows" || (runtime.GOOS == "linux" && runtime.GOARCH == "amd64") {
		return dbinc.BackendBoltDB
	}

	return dbinc.BackendBoltDB
}

func (d *Database) NewTable(tableName string) error {
	if d.fileTable(tableName) != nil {
		return nil
	}
	return d.newTable(tableName)
}

func (d *Database) TableExists(tableName string) bool {
	if d.fileTable(tableName) != nil {
		return true
	}
	return d.tableExists(tableName)
}

func (d *Database) DropTable(tableName string) error {
	if ft := d.fileTable(tableName); ft != nil {
		return ft.drop()
	}
	return d.dropTable(tableName)
}

// Write stores value as JSON under key in tableName.
func (d *Database) Write(tableName string, key string, value interface{}) error {
	if ft := d.fileTable(tableName); ft != nil {
		return ft.write(key, value)
	}
	return d.write(tableName, key, value)
}

// Read decodes the value under key in tableName into assignee.

func (d *Database) Read(tableName string, key string, assignee interface{}) error {
	if ft := d.fileTable(tableName); ft != nil {
		return ft.read(key, assignee)
	}
	return d.read(tableName, key, assignee)
}

func (d *Database) KeyExists(tableName string, key string) bool {
	if ft := d.fileTable(tableName); ft != nil {
		return ft.keyExists(key)
	}
	return d.keyExists(tableName, key)
}

func (d *Database) Delete(tableName string, key string) error {
	if ft := d.fileTable(tableName); ft != nil {
		return ft.delete(key)
	}
	return d.delete(tableName, key)
}

// ListTable returns the [key, value] pairs of a table.

func (d *Database) ListTable(tableName string) ([][][]byte, error) {
	if ft := d.fileTable(tableName); ft != nil {
		return ft.list()
	}
	return d.listTable(tableName)
}

func (d *Database) Close() {
	fileTablesMu.Lock()
	delete(fileTables, d)
	fileTablesMu.Unlock()
	d.close()
}
