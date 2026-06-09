package users

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"sync"
	"time"

	"crypto/rand"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type User struct {
	Name     string `json:"name"`
	Password string `json:"password"` // bcrypt hash (bcrypt stores its own salt)
	Key      string `json:"key"`      // random key for JWT signing
	Quota    string `json:"quota"`    // quota string like "1GiB"
}

type UsersDB struct {
	Users []User `json:"users"`
	Root  string `json:"-"`
	mu    sync.Mutex
}

func (db *UsersDB) Load(path string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			db.Users = []User{}
			return nil
		}
		return err
	}
	return json.Unmarshal(data, db)
}

func (db *UsersDB) SetRoot(path string) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.Root = path
}

// Save writes the DB to path atomically (write-to-tmp + rename) with 0600 permissions.
func (db *UsersDB) Save(path string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	data, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func generateRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func generateKey() (string, error) {
	keyBytes, err := generateRandomBytes(32)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(keyBytes), nil
}

func (db *UsersDB) AddUser(name, password, quota string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	for _, u := range db.Users {
		if u.Name == name {
			log.Printf("User %s already exists", name)
			return errors.New("user already exists")
		}
	}

	hashedPassword, err := hashPassword(password)
	if err != nil {
		log.Printf("Failed to hash password for user %s: %v", name, err)
		return err
	}

	key, err := generateKey()
	if err != nil {
		log.Printf("Failed to generate key for user %s: %v", name, err)
		return err
	}

	newUser := User{
		Name:     name,
		Password: hashedPassword,
		Key:      key,
		Quota:    quota,
	}

	db.Users = append(db.Users, newUser)
	return nil
}

// Authenticate checks name/password without holding the mutex during bcrypt.
func (db *UsersDB) Authenticate(name, password string) bool {
	// Copy the hash under the lock, then compare outside to avoid holding
	// the mutex for the full bcrypt duration (~100 ms).
	db.mu.Lock()
	var hash string
	for _, u := range db.Users {
		if u.Name == name {
			hash = u.Password
			break
		}
	}
	db.mu.Unlock()

	if hash == "" {
		log.Printf("[Authenticate] User %s not found", name)
		return false
	}
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if err != nil {
		log.Printf("[Authenticate] Password mismatch for user %s: %v", name, err)
		return false
	}
	return true
}

// GetUser returns a copy of the User struct to avoid dangling pointers
// after a slice reallocation triggered by AddUser.
func (db *UsersDB) GetUser(name string) *User {
	db.mu.Lock()
	defer db.mu.Unlock()
	for _, u := range db.Users {
		if u.Name == name {
			cp := u
			return &cp
		}
	}
	return nil
}

func (db *UsersDB) GetUsers() ([]string, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	var res []string
	for _, user := range db.Users {
		res = append(res, user.Name)
	}
	return res, nil
}

func (u *User) GenerateJWT() (string, error) {
	claims := jwt.MapClaims{
		"sub": u.Name,
		"exp": time.Now().Add(24 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(u.Key))
}

func (u *User) ValidateJWT(tokenStr string) (*jwt.Token, error) {
	return jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(u.Key), nil
	})
}
