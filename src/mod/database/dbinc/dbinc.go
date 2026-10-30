package dbinc

// BackendType is a database backend.
type BackendType int

const (
	BackendBoltDB BackendType = iota
	BackendFSOnly             // filesystem backend for OpenWrt and RISC-V
	BackendLevelDB

	BackEndAuto = BackendBoltDB
)

type Backend interface {
	NewTable(tableName string) error
	TableExists(tableName string) bool
	DropTable(tableName string) error
	Write(tableName string, key string, value interface{}) error
	Read(tableName string, key string, assignee interface{}) error
	KeyExists(tableName string, key string) bool
	Delete(tableName string, key string) error
	ListTable(tableName string) ([][][]byte, error)
	Close()
}

func (b BackendType) String() string {
	switch b {
	case BackendBoltDB:
		return "BoltDB"
	case BackendFSOnly:
		return "File System Emulated Key-Value Store"
	case BackendLevelDB:
		return "LevelDB"
	default:
		return "Unknown"
	}
}
