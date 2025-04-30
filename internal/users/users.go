package users

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/ioutil"
	"log"
	"os"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type User struct {
	Name     string `json:"name"`
	Password string `json:"password"` // bcrypt hashed password + salt
	Salt     string `json:"salt"`     // random salt
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

	data, err := ioutil.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			db.Users = []User{}
			return nil
		}
		return err
	}
	return json.Unmarshal(data, db)
}

func (db *UsersDB) SetRoot(path string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.Root = path
	return nil
}

func (db *UsersDB) Save(path string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	data, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return err
	}
	return ioutil.WriteFile(path, data, 0644)
}

func generateRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func hashPassword(password, salt string) (string, error) {
	saltedPassword := password + salt
	hash, err := bcrypt.GenerateFromPassword([]byte(saltedPassword), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func generateKey() (string, error) {
	keyBytes, err := generateRandomBytes(32) // 256-bit key
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
			log.Printf("[AddUser] User %s already exists", name)
			return errors.New("user already exists")
		}
	}

	saltBytes, err := generateRandomBytes(32)
	if err != nil {
		log.Printf("[AddUser] Failed to generate salt: %v", err)
		return err
	}
	salt := hex.EncodeToString(saltBytes)

	hashedPassword, err := hashPassword(password, salt)
	if err != nil {
		log.Printf("[AddUser] Failed to hash password for user %s: %v", name, err)
		return err
	}

	key, err := generateKey()
	if err != nil {
		log.Printf("[AddUser] Failed to generate key for user %s: %v", name, err)
		return err
	}

	newUser := User{
		Name:     name,
		Password: hashedPassword,
		Salt:     salt,
		Key:      key,
		Quota:    quota,
	}

	db.Users = append(db.Users, newUser)
	log.Printf("[AddUser] User %s added successfully. Salt: %s, Key: %s", name, salt, key)
	return nil
}

func (db *UsersDB) Authenticate(name, password string) bool {
	db.mu.Lock()
	defer db.mu.Unlock()

	for _, u := range db.Users {
		if u.Name == name {
			saltedPassword := password + u.Salt
			err := bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(saltedPassword))
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

func (db *UsersDB) GetUser(name string) *User {
	db.mu.Lock()
	defer db.mu.Unlock()

	for _, u := range db.Users {
		if u.Name == name {
			return &u
		}
	}
	log.Printf("[Authenticate] User %s not found", name)
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

func (db *UsersDB) GetUsers() ([]string, error) {
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

func (u *User) ValidateJWT(tokenString string) (*jwt.Token, error) {
	return jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(u.Key), nil
	})
}
