package store

// DBType 数据库类型
type DBType int

const (
	MySQL DBType = iota
	PostgreSQL
	SQLite
)

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	Type              DBType
	Host              string
	Port              int
	Database          string
	Username          string
	Password          string
	SSLMode           string // PostgreSQL specific
	File              string // SQLite specific
	MaxOpenConnection string // MySQL, SQLite specific
	MaxIdleConnection string // MySQL, SQLite specific
}

// IsEmpty 检查配置是否为空
func (c DatabaseConfig) IsEmpty() bool {
	return c.Host == "" && c.File == "" && c.Database == ""
}
