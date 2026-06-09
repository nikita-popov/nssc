package users

import (
	"encoding/json"
	"errors"
	"fmt"
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// User represents a single user account.
// Salt field removed: bcrypt embeds a random salt inside the hash itself.
type User struct {
	Name     string `json:"name"`
	Password string `json:"password"` // bcrypt hash (salt embedded)
	Key      string `json:"key"`      // random 256-bit key for JWT signing
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

// SetRoot sets the root directory for the database (no error to return).
func (db *UsersDB) SetRoot(path string) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.Root = path
}

func (db *UsersDB) Save(path string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	data, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func generateRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// hashPassword hashes password using bcrypt (salt is embedded in the hash).
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

func (db *UsersDB) Authenticate(name, password string) bool {
	db.mu.Lock()
	defer db.mu.Unlock()

	for _, u := range db.Users {
		if u.Name == name {
			err := bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(password))
			if err != nil {
				log.Printf("[Authenticate] Password mismatch for user %s: %v", name, err)
				return false
			}
			return true
		}
	}
	log.Printf("[Authenticate] User %s not found", name)
	return false
}

// GetUser returns a pointer to the actual User element in the slice (not a copy).
func (db *UsersDB) GetUser(name string) *User {
	db.mu.Lock()
	defer db.mu.Unlock()

	for i := range db.Users {
		if db.Users[i].Name == name {
			return &db.Users[i]
		}
	}
	log.Printf("[GetUser] User %s not found", name)
	return nil
}

func (db *UsersDB) GetUserKey(name string) (string, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	for _, u := range db.Users {
		if u.Name == name {
			return u.Key, nil
		}
	}
	return "", errors.New("user not found")
}

// GetUsers returns a list of all usernames. Protected by mutex.
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

func (u *User) ValidateJWT(tokenString string) (*jwt.Token, error) {
	return jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(u.Key), nil
	})
}

// GetUsernameFromJWT extracts the "sub" claim from a raw JWT string
// without verifying the signature (used for user lookup before key retrieval).
func GetUsernameFromJWT(tokenString string) (string, error) {
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	token, _, err := parser.ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		return "", fmt.Errorf("parse JWT: %w", err)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", errors.New("invalid JWT claims")
	}
	sub, ok := claims["sub"].(string)
	if !ok || sub == "" {
		return "", errors.New("missing sub claim")
	}
	return sub, nil
}
