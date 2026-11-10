// Encrypted storage. Every file's contents and name are encrypted with AES-256-GCM
// under a random master key. The master key is wrapped by a key derived from the
// password (scrypt), so a password change only re-wraps the key, never re-encrypts files.
// On an imaged drive, a forensic tool sees only random blobs and one encrypted index.
package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/crypto/scrypt"
)

const (
	vaultFolder   = "store"
	indexFileName = "index"
	scryptN       = 1 << 16
	scryptR       = 8
	scryptP       = 1
	keyLength     = 32
)

type vaultEntry struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Month    string `json:"month"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
	Trashed  bool   `json:"trashed"`
}

type vaultIndex struct {
	Entries []vaultEntry `json:"entries"`
}

var (
	vaultMutex sync.Mutex
	masterKey  []byte
	vaultData  *vaultIndex
)

func deriveKey(password string, salt []byte) ([]byte, error) {
	return scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, keyLength)
}

func randomBytes(size int) []byte {
	buffer := make([]byte, size)
	rand.Read(buffer)
	return buffer
}

// seal/open prepend a 12-byte nonce to the GCM ciphertext.
func seal(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := randomBytes(gcm.NonceSize())
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func openSealed(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(data) < gcm.NonceSize() {
		return nil, errors.New("data too short")
	}
	return gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
}

// wrapKey/unwrapKey protect the master key with the password-derived key.
func wrapKey(password string, salt, keyToWrap []byte) (string, error) {
	kek, err := deriveKey(password, salt)
	if err != nil {
		return "", err
	}
	sealed, err := seal(kek, keyToWrap)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sealed), nil
}

func unwrapKey(password string, salt []byte, wrapped string) ([]byte, error) {
	kek, err := deriveKey(password, salt)
	if err != nil {
		return nil, err
	}
	sealed, err := hex.DecodeString(wrapped)
	if err != nil {
		return nil, err
	}
	key, err := openSealed(kek, sealed)
	if err != nil {
		return nil, errors.New("wrong password")
	}
	return key, nil
}

func vaultDirectory() string {
	return filepath.Join(rootDirectory, vaultFolder)
}

func indexPath() string {
	return filepath.Join(rootDirectory, indexFileName)
}

// Called right after login unwraps the master key. Loads (or starts) the index and
// imports any plaintext files an older unencrypted build left behind.
func unlockVault(key []byte) error {
	vaultMutex.Lock()
	defer vaultMutex.Unlock()
	masterKey = key
	if err := os.MkdirAll(vaultDirectory(), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(indexPath())
	if err == nil {
		plain, err := openSealed(masterKey, data)
		if err != nil {
			return errors.New("the locker index couldn’t be opened")
		}
		vaultData = &vaultIndex{}
		if err := json.Unmarshal(plain, vaultData); err != nil {
			return errors.New("the locker index is damaged")
		}
		return nil
	}
	vaultData = &vaultIndex{Entries: []vaultEntry{}}
	if err := saveIndexLocked(); err != nil {
		return err
	}
	return importPlaintextLocked()
}

func lockVault() {
	vaultMutex.Lock()
	masterKey = nil
	vaultData = nil
	vaultMutex.Unlock()
}

func saveIndexLocked() error {
	plain, err := json.Marshal(vaultData)
	if err != nil {
		return err
	}
	sealed, err := seal(masterKey, plain)
	if err != nil {
		return err
	}
	temporary := indexPath() + ".tmp"
	if err := os.WriteFile(temporary, sealed, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, indexPath())
}

func newVaultID() string {
	return hex.EncodeToString(randomBytes(16))
}

// Writes encrypted content and records the entry. Caller holds no lock.
func vaultAdd(name, category, month string, content []byte) (vaultEntry, error) {
	vaultMutex.Lock()
	defer vaultMutex.Unlock()
	sealed, err := seal(masterKey, content)
	if err != nil {
		return vaultEntry{}, err
	}
	id := newVaultID()
	if err := os.WriteFile(filepath.Join(vaultDirectory(), id), sealed, 0o600); err != nil {
		return vaultEntry{}, err
	}
	entry := vaultEntry{
		ID: id, Name: uniqueEntryName(category, month, name), Category: category, Month: month,
		Size: int64(len(content)), Modified: time.Now().UnixMilli(), Trashed: false,
	}
	vaultData.Entries = append(vaultData.Entries, entry)
	if err := saveIndexLocked(); err != nil {
		os.Remove(filepath.Join(vaultDirectory(), id))
		vaultData.Entries = vaultData.Entries[:len(vaultData.Entries)-1]
		return vaultEntry{}, err
	}
	return entry, nil
}

func uniqueEntryName(category, month, name string) string {
	extension := filepath.Ext(name)
	base := name[:len(name)-len(extension)]
	candidate := name
	for counter := 2; ; counter++ {
		taken := false
		for _, entry := range vaultData.Entries {
			if entry.Category == category && entry.Month == month && entry.Name == candidate {
				taken = true
				break
			}
		}
		if !taken {
			return candidate
		}
		candidate = base + " (" + itoa(counter) + ")" + extension
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

func findEntry(category, month, name string, trashed bool) *vaultEntry {
	vaultMutex.Lock()
	defer vaultMutex.Unlock()
	for index := range vaultData.Entries {
		entry := &vaultData.Entries[index]
		if entry.Category == category && entry.Month == month && entry.Name == name && entry.Trashed == trashed {
			return entry
		}
	}
	return nil
}

func vaultList(trashed bool) []fileEntry {
	vaultMutex.Lock()
	defer vaultMutex.Unlock()
	files := []fileEntry{}
	for _, entry := range vaultData.Entries {
		if entry.Trashed != trashed {
			continue
		}
		files = append(files, fileEntry{
			Name: entry.Name, Category: entry.Category, Month: entry.Month,
			Size: entry.Size, Modified: entry.Modified,
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Modified > files[j].Modified })
	return files
}

func vaultTotalSize() int64 {
	vaultMutex.Lock()
	defer vaultMutex.Unlock()
	var total int64
	for _, entry := range vaultData.Entries {
		if !entry.Trashed {
			total += entry.Size
		}
	}
	return total
}

func readEntryContent(category, month, name string, trashed bool) ([]byte, error) {
	entry := findEntry(category, month, name, trashed)
	if entry == nil {
		return nil, errors.New("file not found")
	}
	vaultMutex.Lock()
	id := entry.ID
	vaultMutex.Unlock()
	sealed, err := os.ReadFile(filepath.Join(vaultDirectory(), id))
	if err != nil {
		return nil, err
	}
	return openSealed(masterKey, sealed)
}

func setTrashed(category, month, name string, from, to bool) error {
	vaultMutex.Lock()
	defer vaultMutex.Unlock()
	for index := range vaultData.Entries {
		entry := &vaultData.Entries[index]
		if entry.Category == category && entry.Month == month && entry.Name == name && entry.Trashed == from {
			entry.Trashed = to
			return saveIndexLocked()
		}
	}
	return errors.New("file not found")
}

func purgeEntry(category, month, name string) error {
	vaultMutex.Lock()
	defer vaultMutex.Unlock()
	for index := range vaultData.Entries {
		entry := &vaultData.Entries[index]
		if entry.Category == category && entry.Month == month && entry.Name == name && entry.Trashed {
			os.Remove(filepath.Join(vaultDirectory(), entry.ID))
			vaultData.Entries = append(vaultData.Entries[:index], vaultData.Entries[index+1:]...)
			return saveIndexLocked()
		}
	}
	return errors.New("file not found")
}

func renameEntry(category, month, name, newName string) (string, error) {
	vaultMutex.Lock()
	defer vaultMutex.Unlock()
	for index := range vaultData.Entries {
		entry := &vaultData.Entries[index]
		if entry.Category == category && entry.Month == month && entry.Name == name && !entry.Trashed {
			final := uniqueEntryName(category, month, newName)
			entry.Name = final
			if err := saveIndexLocked(); err != nil {
				return "", err
			}
			return final, nil
		}
	}
	return "", errors.New("file not found")
}

// Replaces an entry's content after a metadata edit, keeping its id and name.
func replaceEntryContent(category, month, name string, content []byte) error {
	entry := findEntry(category, month, name, false)
	if entry == nil {
		return errors.New("file not found")
	}
	sealed, err := seal(masterKey, content)
	if err != nil {
		return err
	}
	vaultMutex.Lock()
	defer vaultMutex.Unlock()
	if err := os.WriteFile(filepath.Join(vaultDirectory(), entry.ID), sealed, 0o600); err != nil {
		return err
	}
	for index := range vaultData.Entries {
		if vaultData.Entries[index].ID == entry.ID {
			vaultData.Entries[index].Size = int64(len(content))
			vaultData.Entries[index].Modified = time.Now().UnixMilli()
		}
	}
	return saveIndexLocked()
}

// Decrypts an entry to a throwaway file in the OS temp area (never the USB),
// runs fn on it, and shreds it afterwards. reEncrypt re-imports any edits fn made.
func withDecryptedTemp(category, month, name string, trashed, reEncrypt bool, fn func(path string) error) error {
	content, err := readEntryContent(category, month, name, trashed)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp("", "dpl-*"+filepath.Ext(name))
	if err != nil {
		return err
	}
	path := temporary.Name()
	defer shredFile(path)
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	temporary.Close()
	if err := fn(path); err != nil {
		return err
	}
	if reEncrypt {
		edited, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return replaceEntryContent(category, month, name, edited)
	}
	return nil
}

// Overwrites a temp file with random bytes before deleting, so the plaintext
// doesn't linger in free space.
func shredFile(path string) {
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		if file, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
			io.CopyN(file, rand.Reader, info.Size())
			file.Sync()
			file.Close()
		}
	}
	os.Remove(path)
}

// Imports files written by an older unencrypted build into the vault, then removes
// the plaintext originals. Runs once, under vaultMutex.
func importPlaintextLocked() error {
	for _, item := range categories {
		for _, root := range []struct {
			base    string
			trashed bool
		}{{rootDirectory, false}, {filepath.Join(rootDirectory, trashFolder), true}} {
			months, err := os.ReadDir(filepath.Join(root.base, item.Folder))
			if err != nil {
				continue
			}
			for _, month := range months {
				if !month.IsDir() {
					continue
				}
				monthDir := filepath.Join(root.base, item.Folder, month.Name())
				files, _ := os.ReadDir(monthDir)
				for _, file := range files {
					if file.IsDir() {
						continue
					}
					path := filepath.Join(monthDir, file.Name())
					content, err := os.ReadFile(path)
					if err != nil {
						continue
					}
					sealed, err := seal(masterKey, content)
					if err != nil {
						return err
					}
					id := newVaultID()
					if err := os.WriteFile(filepath.Join(vaultDirectory(), id), sealed, 0o600); err != nil {
						return err
					}
					info, _ := file.Info()
					modified := time.Now().UnixMilli()
					if info != nil {
						modified = info.ModTime().UnixMilli()
					}
					vaultData.Entries = append(vaultData.Entries, vaultEntry{
						ID: id, Name: file.Name(), Category: item.Folder, Month: month.Name(),
						Size: int64(len(content)), Modified: modified, Trashed: root.trashed,
					})
					shredFile(path)
				}
			}
			os.RemoveAll(filepath.Join(root.base, item.Folder))
		}
	}
	return saveIndexLocked()
}

var _ = sha256.Size
