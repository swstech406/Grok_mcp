// sqlite.go 实现 Store 接口：WAL 模式 SQLite、UUID 风格主键、grok_ 前缀的随机 API Key。
package store

import (
	"database/sql"
	"fmt"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/keycrypt"

	_ "modernc.org/sqlite"
)

const sqliteReadPoolSize = 4

const sqliteCommonPragmas = "&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"

// SQLiteStore 使用纯 Go 驱动 modernc.org/sqlite。写连接保持串行，读取连接池用于发挥 WAL 的并发读取能力。
type SQLiteStore struct {
	db           *sql.DB
	readDB       *sql.DB
	debugDB      *sql.DB
	secretCipher *keycrypt.Cipher
	metrics      sqliteMetrics
}

// OpenSQLite 打开数据库、执行嵌入迁移并返回可用的 Store。
func OpenSQLite(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"+sqliteCommonPragmas)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	debugDB, err := openDebugSQLite(path)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if path == ":memory:" {
		return &SQLiteStore{db: db, readDB: db, debugDB: debugDB}, nil
	}

	readDB, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=query_only(1)"+sqliteCommonPragmas)
	if err != nil {
		_ = debugDB.Close()
		_ = db.Close()
		return nil, fmt.Errorf("open sqlite read pool: %w", err)
	}
	readDB.SetMaxOpenConns(sqliteReadPoolSize)
	readDB.SetMaxIdleConns(sqliteReadPoolSize)
	if err := readDB.Ping(); err != nil {
		_ = readDB.Close()
		_ = debugDB.Close()
		_ = db.Close()
		return nil, fmt.Errorf("initialize sqlite read pool: %w", err)
	}

	return &SQLiteStore{db: db, readDB: readDB, debugDB: debugDB}, nil
}

func (s *SQLiteStore) Close() error {
	var firstCloseErr error
	if s.debugDB != nil {
		firstCloseErr = s.debugDB.Close()
	}
	if s.readDB != nil && s.readDB != s.db {
		if err := s.readDB.Close(); err != nil && firstCloseErr == nil {
			firstCloseErr = err
		}
	}
	if s.db != nil {
		if err := s.db.Close(); err != nil && firstCloseErr == nil {
			firstCloseErr = err
		}
	}
	return firstCloseErr
}
