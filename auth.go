// Password + authenticator-app (TOTP, RFC 6238) lock for DPLocker.
// Works with any standard authenticator: Authenticator extension, Google/Microsoft Authenticator.
package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"
)

const (
	authFileName       = "auth.json"
	totpIssuer         = "DPLocker"
	totpPeriodSeconds  = 30
	totpAllowedDrift   = 1
	minPasswordLength  = 6
	maxFailedAttempts  = 5
	baseLockoutSeconds = 30
)

type authData struct {
	PasswordHash string `json:"passwordHash"`
	TotpSecret   string `json:"totpSecret"`
	AccountName  string `json:"accountName"`
	KdfSalt      string `json:"kdfSalt"`
	WrappedKey   string `json:"wrappedKey"`
}

// Ensures the account has a wrapped master key, creating one for lockers set up by an
// older unencrypted build, then returns the unwrapped key. Caller holds authMutex.
func ensureMasterKey(data *authData, password string) ([]byte, error) {
	if data.KdfSalt == "" || data.WrappedKey == "" {
		salt := randomBytes(16)
		key := randomBytes(keyLength)
		wrapped, err := wrapKey(password, salt, key)
		if err != nil {
			return nil, err
		}
		data.KdfSalt = hex.EncodeToString(salt)
		data.WrappedKey = wrapped
		if err := saveAuth(data); err != nil {
			return nil, err
		}
		return key, nil
	}
	salt, err := hex.DecodeString(data.KdfSalt)
	if err != nil {
		return nil, err
	}
	return unwrapKey(password, salt, data.WrappedKey)
}

var (
	authMutex       sync.Mutex
	sessionToken    string
	pendingSecret   string
	lastUsedStep    int64
	failedAttempts  int
	lockedUntil     time.Time
	base32NoPadding = base32.StdEncoding.WithPadding(base32.NoPadding)
)

func authPath() string {
	return filepath.Join(rootDirectory, authFileName)
}

func loadAuth() (*authData, bool) {
	bytes, err := os.ReadFile(authPath())
	if err != nil {
		return nil, false
	}
	var data authData
	if json.Unmarshal(bytes, &data) != nil || data.PasswordHash == "" || data.TotpSecret == "" {
		return nil, false
	}
	return &data, true
}

func saveAuth(data *authData) error {
	bytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	// Write then rename so a pulled USB mid-save can't leave a half-written auth file.
	temporary := authPath() + ".tmp"
	if err := os.WriteFile(temporary, bytes, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, authPath())
}

func newSecret() string {
	bytes := make([]byte, 20)
	rand.Read(bytes)
	return base32NoPadding.EncodeToString(bytes)
}

func totpCode(secret string, step int64) string {
	key, err := base32NoPadding.DecodeString(strings.ToUpper(secret))
	if err != nil {
		return ""
	}
	message := make([]byte, 8)
	binary.BigEndian.PutUint64(message, uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(message)
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000)
}

// Returns the matched time step so a code can't be replayed within its window.
func verifyTotp(secret, code string) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return 0, false
	}
	current := time.Now().Unix() / totpPeriodSeconds
	for drift := int64(-totpAllowedDrift); drift <= totpAllowedDrift; drift++ {
		step := current + drift
		if hmac.Equal([]byte(totpCode(secret, step)), []byte(code)) {
			return step, true
		}
	}
	return 0, false
}

func otpauthURI(secret, account string) string {
	label := url.PathEscape(totpIssuer + ":" + account)
	return fmt.Sprintf("otpauth://totp/%s?secret=%s&issuer=%s&algorithm=SHA1&digits=6&period=%d",
		label, secret, url.QueryEscape(totpIssuer), totpPeriodSeconds)
}

func qrDataURI(content string) string {
	png, err := qrcode.Encode(content, qrcode.Medium, 280)
	if err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

func readJSON(request *http.Request, target any) bool {
	return json.NewDecoder(http.MaxBytesReader(nil, request.Body, 64*1024)).Decode(target) == nil
}

// Must be called with authMutex held. Lockout doubles each time it triggers.
func checkLockout() (bool, string) {
	if time.Now().Before(lockedUntil) {
		seconds := int(time.Until(lockedUntil).Seconds()) + 1
		return true, fmt.Sprintf("Too many attempts. Try again in %d seconds.", seconds)
	}
	return false, ""
}

func registerFailure() {
	failedAttempts++
	if failedAttempts%maxFailedAttempts == 0 {
		multiplier := 1 << (failedAttempts/maxFailedAttempts - 1)
		lockedUntil = time.Now().Add(time.Duration(baseLockoutSeconds*multiplier) * time.Second)
	}
}

// Checks password + code against stored auth. Must be called with authMutex held.
func verifyCredentials(data *authData, password, code string) (bool, string) {
	if locked, message := checkLockout(); locked {
		return false, message
	}
	passwordOk := bcrypt.CompareHashAndPassword([]byte(data.PasswordHash), []byte(password)) == nil
	step, codeOk := verifyTotp(data.TotpSecret, code)
	if !passwordOk || !codeOk || step <= lastUsedStep {
		registerFailure()
		if passwordOk && codeOk {
			return false, "That code was already used. Wait for the next one."
		}
		return false, "Wrong password or code."
	}
	lastUsedStep = step
	failedAttempts = 0
	return true, ""
}

func startSession() string {
	sessionToken = randomToken()
	return sessionToken
}

// File APIs need a session from a successful login on top of the launch token.
func requireSession(next http.HandlerFunc) http.HandlerFunc {
	return requireToken(func(writer http.ResponseWriter, request *http.Request) {
		provided := request.Header.Get("X-Session")
		if provided == "" {
			provided = request.URL.Query().Get("s")
		}
		authMutex.Lock()
		valid := sessionToken != "" && hmac.Equal([]byte(provided), []byte(sessionToken))
		authMutex.Unlock()
		if !valid {
			http.Error(writer, "locked", http.StatusUnauthorized)
			return
		}
		next(writer, request)
	})
}

func handleAuthStatus(writer http.ResponseWriter, request *http.Request) {
	if rootDirectory == "" {
		writeJSON(writer, map[string]any{"needsDrive": true})
		return
	}
	_, configured := loadAuth()
	writeJSON(writer, map[string]any{"configured": configured, "drive": storageBase, "portable": portable})
}

func handleSetupBegin(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		AccountName string `json:"accountName"`
	}
	readJSON(request, &body)
	authMutex.Lock()
	defer authMutex.Unlock()
	// Setup without a session is only allowed before a password exists.
	if _, configured := loadAuth(); configured && request.Header.Get("X-Session") != sessionToken {
		http.Error(writer, "already configured", http.StatusForbidden)
		return
	}
	account := strings.TrimSpace(body.AccountName)
	if account == "" {
		account = "USB Drive"
	}
	pendingSecret = newSecret()
	uri := otpauthURI(pendingSecret, account)
	writeJSON(writer, map[string]any{"secret": pendingSecret, "uri": uri, "qr": qrDataURI(uri)})
}

func handleSetupFinish(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Password    string `json:"password"`
		Code        string `json:"code"`
		AccountName string `json:"accountName"`
	}
	if !readJSON(request, &body) {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	authMutex.Lock()
	defer authMutex.Unlock()
	if _, configured := loadAuth(); configured {
		http.Error(writer, "already configured", http.StatusForbidden)
		return
	}
	if len(body.Password) < minPasswordLength {
		writeJSON(writer, map[string]any{"ok": false, "message": fmt.Sprintf("Password needs at least %d characters.", minPasswordLength)})
		return
	}
	step, ok := verifyTotp(pendingSecret, body.Code)
	if pendingSecret == "" || !ok {
		writeJSON(writer, map[string]any{"ok": false, "message": "That code doesn’t match. Check the authenticator and try again."})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(writer, map[string]any{"ok": false, "message": "Could not secure password."})
		return
	}
	data := &authData{PasswordHash: string(hash), TotpSecret: pendingSecret, AccountName: body.AccountName}
	if err := saveAuth(data); err != nil {
		writeJSON(writer, map[string]any{"ok": false, "message": "Could not save settings: " + err.Error()})
		return
	}
	key, err := ensureMasterKey(data, body.Password)
	if err != nil {
		writeJSON(writer, map[string]any{"ok": false, "message": "Could not set up encryption."})
		return
	}
	if err := unlockVault(key); err != nil {
		writeJSON(writer, map[string]any{"ok": false, "message": "Could not open the locker: " + err.Error()})
		return
	}
	pendingSecret = ""
	lastUsedStep = step
	writeJSON(writer, map[string]any{"ok": true, "session": startSession()})
}

func handleLogin(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !readJSON(request, &body) {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	authMutex.Lock()
	defer authMutex.Unlock()
	data, configured := loadAuth()
	if !configured {
		writeJSON(writer, map[string]any{"ok": false, "message": "Not set up yet."})
		return
	}
	if ok, message := verifyCredentials(data, body.Password, body.Code); !ok {
		writeJSON(writer, map[string]any{"ok": false, "message": message})
		return
	}
	key, err := ensureMasterKey(data, body.Password)
	if err != nil {
		writeJSON(writer, map[string]any{"ok": false, "message": "Could not unlock encryption."})
		return
	}
	if err := unlockVault(key); err != nil {
		writeJSON(writer, map[string]any{"ok": false, "message": "Could not open the locker: " + err.Error()})
		return
	}
	writeJSON(writer, map[string]any{"ok": true, "session": startSession()})
}

func handleLogout(writer http.ResponseWriter, request *http.Request) {
	authMutex.Lock()
	sessionToken = ""
	authMutex.Unlock()
	lockVault()
	writer.WriteHeader(http.StatusNoContent)
}

func handleChangePassword(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		Code            string `json:"code"`
		NewPassword     string `json:"newPassword"`
	}
	if !readJSON(request, &body) {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	authMutex.Lock()
	defer authMutex.Unlock()
	data, _ := loadAuth()
	if len(body.NewPassword) < minPasswordLength {
		writeJSON(writer, map[string]any{"ok": false, "message": fmt.Sprintf("New password needs at least %d characters.", minPasswordLength)})
		return
	}
	if ok, message := verifyCredentials(data, body.CurrentPassword, body.Code); !ok {
		writeJSON(writer, map[string]any{"ok": false, "message": message})
		return
	}
	// Re-wrap the master key under the new password so files never need re-encrypting.
	salt, err := hex.DecodeString(data.KdfSalt)
	if err != nil {
		writeJSON(writer, map[string]any{"ok": false, "message": "Could not read encryption settings."})
		return
	}
	key, err := unwrapKey(body.CurrentPassword, salt, data.WrappedKey)
	if err != nil {
		writeJSON(writer, map[string]any{"ok": false, "message": "Wrong password or code."})
		return
	}
	newSalt := randomBytes(16)
	wrapped, err := wrapKey(body.NewPassword, newSalt, key)
	if err != nil {
		writeJSON(writer, map[string]any{"ok": false, "message": "Could not re-encrypt the key."})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(writer, map[string]any{"ok": false, "message": "Could not secure password."})
		return
	}
	data.PasswordHash = string(hash)
	data.KdfSalt = hex.EncodeToString(newSalt)
	data.WrappedKey = wrapped
	if err := saveAuth(data); err != nil {
		writeJSON(writer, map[string]any{"ok": false, "message": "Could not save: " + err.Error()})
		return
	}
	writeJSON(writer, map[string]any{"ok": true})
}

// Switches to a new authenticator secret; needs the password and a code from the NEW entry.
func handleRelinkFinish(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Password    string `json:"password"`
		Code        string `json:"code"`
		AccountName string `json:"accountName"`
	}
	if !readJSON(request, &body) {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	authMutex.Lock()
	defer authMutex.Unlock()
	data, _ := loadAuth()
	if locked, message := checkLockout(); locked {
		writeJSON(writer, map[string]any{"ok": false, "message": message})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(data.PasswordHash), []byte(body.Password)) != nil {
		registerFailure()
		writeJSON(writer, map[string]any{"ok": false, "message": "Wrong password."})
		return
	}
	step, ok := verifyTotp(pendingSecret, body.Code)
	if pendingSecret == "" || !ok {
		writeJSON(writer, map[string]any{"ok": false, "message": "That code doesn’t match the new QR code."})
		return
	}
	data.TotpSecret = pendingSecret
	data.AccountName = body.AccountName
	if err := saveAuth(data); err != nil {
		writeJSON(writer, map[string]any{"ok": false, "message": "Could not save: " + err.Error()})
		return
	}
	pendingSecret = ""
	lastUsedStep = step
	writeJSON(writer, map[string]any{"ok": true})
}
