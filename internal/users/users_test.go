package users_test

import (
	"nssc/internal/users"
	"os"
	"testing"
)

func TestUserManagement(t *testing.T) {
	dbPath := "test_db.json"
	defer os.Remove(dbPath)

	t.Run("Add and authenticate user", func(t *testing.T) {
		db := users.UsersDB{}
		err := db.AddUser("test", "password", "1GiB")
		if err != nil {
			t.Fatal(err)
		}

		if !db.Authenticate("test", "password") {
			t.Error("Authentication failed")
		}

		if db.Authenticate("test", "wrong") {
			t.Error("Invalid authentication")
		}
	})

	t.Run("Save and load DB", func(t *testing.T) {
		db1 := users.UsersDB{}
		db1.AddUser("user1", "pass1", "1GB")
		db1.Save(dbPath)

		db2 := users.UsersDB{}
		if err := db2.Load(dbPath); err != nil {
			t.Fatal(err)
		}

		if len(db2.Users) != 1 {
			t.Error("DB load failed")
		}
	})
}
