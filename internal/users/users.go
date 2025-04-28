package users

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/dustin/go-humanize"
	"golang.org/x/crypto/bcrypt"
)

type User struct {
	Name     string `json:"name"`
	Password string `json:"password"` // bcrypt hashed password with salt
	Salt     string `json:"salt"`     // random salt, base64 encoded
	Key      string `json:"key"`      // random key for JWT signing, base64 encoded
	Quota    string `json:"quota"`    // quota string like "1GiB"
}

type UsersDB struct {
	Users []User `json:"users"`
	Root  string
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
	return base64.StdEncoding.EncodeToString(keyBytes), nil
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

	saltBytes, err := generateRandomBytes(16)
	if err != nil {
		log.Printf("[AddUser] Failed to generate salt: %v", err)
		return err
	}
	salt := base64.StdEncoding.EncodeToString(saltBytes)

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

func (db *UsersDB) GetQuota(username string) (used uint64, total uint64, err error) {
	var (
		user  User
		found bool = false
	)
	for _, user = range db.Users {
		if user.Name == username {
			found = true
			break
		}
	}
	if !found {
		return 0, 0, errors.New("user not found")
	}

	total, err = humanize.ParseBytes(user.Quota)
	if err != nil {
		return 0, 0, err
	}

	userDir := filepath.Join(db.Root, "users", username)
	err = filepath.Walk(userDir, func(path string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			used += uint64(info.Size())
		}
		return nil
	})

	return used, total, err
}

func (db *UsersDB) GetUsers() ([]string, error) {
	var res []string

	for _, user := range db.Users {
		res = append(res, user.Name)
	}
	return res, nil
}
