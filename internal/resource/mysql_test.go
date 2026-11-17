package resource_test

import (
	"os"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/samuelncui/yatm/internal/resource/resourcetest"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestMySQLCatalogRequiresANamedDatabase(t *testing.T) {
	_, err := resource.NewDBConn("mysql", "root:secret@tcp(127.0.0.1:3306)/")
	require.ErrorContains(t, err, "must name a database")
	require.NotContains(t, err.Error(), "secret")
}

func TestMySQLCatalogSessionAndCollation(t *testing.T) {
	server := os.Getenv(resourcetest.MySQLEnv)
	if server == "" {
		t.Skip(resourcetest.MySQLEnv + " is not set")
	}

	// Every pooled session compares bytes and reads committed data.
	db := resourcetest.OpenCatalog(t)
	var session struct {
		Isolation string `gorm:"column:isolation"`
		Collation string `gorm:"column:collation"`
	}
	require.NoError(t, db.Raw("SELECT @@transaction_isolation AS isolation, @@collation_connection AS collation").
		Scan(&session).Error)
	require.Equal(t, "READ-COMMITTED", session.Isolation)
	require.Equal(t, resource.MySQLCollation, session.Collation)

	// A database whose default collation folds case or accents is refused before any table exists.
	cfg, err := mysqldriver.ParseDSN(server)
	require.NoError(t, err)
	admin, err := gorm.Open(mysql.Open(server))
	require.NoError(t, err)
	t.Cleanup(func() { _ = resource.CloseDB(admin) })
	cfg.DBName = "yatm_test_folding_collation"
	require.NoError(t, admin.Exec("DROP DATABASE IF EXISTS "+cfg.DBName).Error)
	require.NoError(t, admin.Exec("CREATE DATABASE "+cfg.DBName+" CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci").Error)
	t.Cleanup(func() { _ = admin.Exec("DROP DATABASE IF EXISTS " + cfg.DBName).Error })
	_, err = resource.NewDBConn("mysql", cfg.FormatDSN())
	require.ErrorContains(t, err, "collation is utf8mb4_0900_ai_ci, expected utf8mb4_bin")
}
