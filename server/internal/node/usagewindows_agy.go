package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	agyStatusTimeout  = 1500 * time.Millisecond
	agyStatusCacheTTL = 5 * time.Minute
)

var (
	agyNativeHTTPPortRE = lazyre.New(`(?m)random port at (\d+) for HTTP(?:\r?\n|$)`)
	agyLocalUserStatus  = probeLocalAgyUserStatus
)

type agyUserIdentity struct {
	name       string
	plan       string
	accountKey string
}

var agyIdentityCache struct {
	mu      sync.Mutex
	at      time.Time
	checked bool
	value   agyUserIdentity
}

// enrichLegacyAgyUsage backfills signed-in account name, subscription plan, and SHA-256 account_key from the local
// Antigravity language-server HTTP endpoint when an older running companion (kept alive for an active turn) reports a
// generic "Antigravity" sample without account identity. Updated companions already send name, plan, and account_key
// directly, so this is a no-op for them.
func enrichLegacyAgyUsage(a *proto.UsageAccount, accountKey *string, now time.Time) {
	p := strings.ToLower(strings.TrimSpace(a.Provider))
	if (p != "agy" && p != "antigravity") || *accountKey != "" || !strings.EqualFold(strings.TrimSpace(a.Name), "Antigravity") {
		return
	}
	id, ok := cachedAgyUserStatus(now)
	if !ok {
		return
	}
	if id.name != "" {
		a.Name = id.name
	}
	if a.Plan == "" && id.plan != "" {
		a.Plan = id.plan
	}
	if id.accountKey != "" {
		*accountKey = id.accountKey
	}
}

func cachedAgyUserStatus(now time.Time) (agyUserIdentity, bool) {
	agyIdentityCache.mu.Lock()
	if agyIdentityCache.checked && now.Sub(agyIdentityCache.at) < agyStatusCacheTTL {
		val := agyIdentityCache.value
		agyIdentityCache.mu.Unlock()
		return val, val.name != "" || val.accountKey != ""
	}
	agyIdentityCache.mu.Unlock()

	val, ok := agyLocalUserStatus()
	agyIdentityCache.mu.Lock()
	agyIdentityCache.at = now
	agyIdentityCache.checked = true
	if ok {
		agyIdentityCache.value = val
	} else {
		agyIdentityCache.value = agyUserIdentity{}
	}
	agyIdentityCache.mu.Unlock()
	return val, ok
}

func probeLocalAgyUserStatus() (agyUserIdentity, bool) {
	logs, _ := filepath.Glob(filepath.Join(os.TempDir(), "persona-agy-*", "native.log"))
	if len(logs) == 0 {
		return agyUserIdentity{}, false
	}
	client := &http.Client{
		Timeout:   agyStatusTimeout,
		Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
	}
	for i := len(logs) - 1; i >= 0; i-- {
		raw, err := os.ReadFile(logs[i])
		if err != nil {
			continue
		}
		m := agyNativeHTTPPortRE.FindSubmatch(raw)
		if m == nil {
			continue
		}
		port, err := strconv.Atoi(string(m[1]))
		if err != nil || port <= 0 || port > 65535 {
			continue
		}
		if id, ok := queryAgyUserStatusPort(client, port); ok {
			return id, true
		}
	}
	return agyUserIdentity{}, false
}

func queryAgyUserStatusPort(client *http.Client, port int) (agyUserIdentity, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), agyStatusTimeout)
	defer cancel()
	body := []byte(`{"metadata":{}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(port)+"/exa.language_server_pb.LanguageServerService/GetUserStatus", bytes.NewReader(body))
	if err != nil {
		return agyUserIdentity{}, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	resp, err := client.Do(req)
	if err != nil {
		return agyUserIdentity{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return agyUserIdentity{}, false
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return agyUserIdentity{}, false
	}
	return parseAgyUserStatusJSON(payload)
}

func parseAgyUserStatusJSON(payload []byte) (agyUserIdentity, bool) {
	var env struct {
		UserStatus *struct {
			Email      string `json:"email"`
			Name       string `json:"name"`
			PlanStatus struct {
				PlanInfo struct {
					PlanName string `json:"planName"`
				} `json:"planInfo"`
			} `json:"planStatus"`
			UserTier struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"userTier"`
		} `json:"userStatus"`
		PlanInfo struct {
			PlanName string `json:"planName"`
		} `json:"planInfo"`
	}
	if json.Unmarshal(payload, &env) != nil || env.UserStatus == nil {
		return agyUserIdentity{}, false
	}
	us := env.UserStatus
	rawEmail := cleanAgyText(us.Email, 0)
	name := cleanAgyText(us.Email, 128)
	if name == "" {
		name = cleanAgyText(us.Name, 128)
	}
	plan := cleanAgyText(us.UserTier.Name, 128)
	if plan == "" {
		plan = cleanAgyText(us.PlanStatus.PlanInfo.PlanName, 128)
	}
	if plan == "" {
		plan = cleanAgyText(env.PlanInfo.PlanName, 128)
	}
	if plan == "" {
		plan = cleanAgyText(us.UserTier.ID, 128)
	}
	accountKey := ""
	if rawEmail != "" {
		sum := sha256.Sum256([]byte("agy:" + strings.ToLower(rawEmail)))
		accountKey = hex.EncodeToString(sum[:])
	}
	if name == "" && accountKey == "" {
		return agyUserIdentity{}, false
	}
	return agyUserIdentity{name: name, plan: plan, accountKey: accountKey}, true
}

func cleanAgyText(s string, maxBytes int) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if r <= 0x1f || r == 0x7f || r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	cleaned := strings.TrimSpace(b.String())
	if maxBytes <= 0 || len(cleaned) <= maxBytes {
		return cleaned
	}
	n := 0
	for i := range cleaned {
		_, w := utf8.DecodeRuneInString(cleaned[i:])
		if i+w > maxBytes {
			break
		}
		n = i + w
	}
	return strings.TrimSpace(cleaned[:n])
}
