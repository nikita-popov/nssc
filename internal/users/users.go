package users

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// authCacheTTL bounds how long a successful bcrypt comparison is reused.
// bcrypt at the default cost takes ~60-100 ms, which dominates request
// latency for WebDAV clients that authenticate on every request (rclone with
// parallel transfers).
const authCacheTTL = 60 * time.Second

// authCacheMaxEntries caps memory used by the cache; a full cache is simply
// dropped, since entries are only an optimisation.
const authCacheMaxEntries = 1024

type User struct {
	Name     string `json:"name"`
	Password string `json:"password"` // bcrypt hash (bcrypt stores its own salt)
	Key      string `json:"key"`      // random key for JWT signing
	Quota    string `json:"quota"`    // quota string like "1GiB"
}

type authEntry struct {
	ok  bool
	exp time.Time
}

type UsersDB struct {
	Users []User `json:"users"`
	Root  string `json:"-"`
	mu    sync.Mutex

	authCache map[string]authEntry
}

// ValidName reports whether name is safe to use as a username and as a
// directory name. It rejects empty names, path separators, control characters
// and the reserved top-level entries of the storage layout.
func ValidName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if len(name) > 64 {
		return false
	}
	if strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	switch strings.ToLower(name) {
	case "public", "user", "db.json":
		return false
	}
	return true
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
	if _, err := rand.Read(b); err != nil {
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
	if !ValidName(name) {
		return errors.New("invalid username")
	}
	if password == "" {
		return errors.New("empty password")
	}

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

	db.Users = append(db.Users, User{
		Name:     name,
		Password: hashedPassword,
		Key:      key,
		Quota:    quota,
	})
	// Any cached credential result may now be stale (e.g. re-added user).
	db.authCache = nil
	return nil
}

// Authenticate checks name/password, reusing a recent successful result so a
// chatty client does not pay for bcrypt on every request.
func (db *UsersDB) Authenticate(name, password string) bool {
	sum := sha256.Sum256([]byte(name + "\x00" + password))
	cacheKey := string(sum[:])
	now := time.Now()

	db.mu.Lock()
	if e, ok := db.authCache[cacheKey]; ok && now.Before(e.exp) {
		db.mu.Unlock()
		return e.ok
	}
	// Copy the hash under the lock, then compare outside: bcrypt must not be
	// run while holding the mutex.
	var hash string
	for _, u := range db.Users {
		if u.Name == name {
			hash = u.Password
			break
		}
	}
	db.mu.Unlock()

	ok := false
	if hash != "" {
		ok = bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	}

	db.mu.Lock()
	if len(db.authCache) >= authCacheMaxEntries {
		db.authCache = make(map[string]authEntry, authCacheMaxEntries)
	}
	if db.authCache == nil {
		db.authCache = make(map[string]authEntry, authCacheMaxEntries)
	}
	// Only successful logins are cached; failures stay expensive on purpose
	// so a wrong password is never cheap to brute-force.
	if ok {
		db.authCache[cacheKey] = authEntry{ok: true, exp: now.Add(authCacheTTL)}
	}
	db.mu.Unlock()
	return ok
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
	res := make([]string, 0, len(db.Users))
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
