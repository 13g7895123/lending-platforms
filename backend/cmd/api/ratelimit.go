package main

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// rateLimiter 是記憶體 token bucket。
//
// 限制：狀態只存在單一行程內，多副本部署時每個副本各自計數，
// 實際允許量會是設定值乘以副本數。要精確限流需改用 Redis 等共享存放，
// 目前單副本部署下足夠，此限制記錄於 README。
type rateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	capacity float64
	refill   float64 // 每秒補充的 token 數
	lastGC   time.Time
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

// newRateLimiter 建立限流器：capacity 為突發上限，perWindow 為每 window 允許的請求數。
func newRateLimiter(capacity int, perWindow int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		buckets:  make(map[string]*bucket),
		capacity: float64(capacity),
		refill:   float64(perWindow) / window.Seconds(),
		lastGC:   time.Now(),
	}
}

// allow 消耗一個 token。回傳是否允許，以及建議的重試等待秒數。
func (l *rateLimiter) allow(key string) (bool, int) {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.collectGarbageLocked(now)

	entry, found := l.buckets[key]
	if !found {
		l.buckets[key] = &bucket{tokens: l.capacity - 1, lastSeen: now}
		return true, 0
	}

	// 依經過時間補充 token，上限為 capacity
	elapsed := now.Sub(entry.lastSeen).Seconds()
	entry.tokens += elapsed * l.refill
	if entry.tokens > l.capacity {
		entry.tokens = l.capacity
	}
	entry.lastSeen = now

	if entry.tokens < 1 {
		// 還需要多久才會累積出 1 個 token
		wait := int((1 - entry.tokens) / l.refill)
		if wait < 1 {
			wait = 1
		}
		return false, wait
	}
	entry.tokens--
	return true, 0
}

// collectGarbageLocked 清掉長時間未使用的 bucket，避免記憶體無上限成長。
// 呼叫端必須已持有 mu。
func (l *rateLimiter) collectGarbageLocked(now time.Time) {
	if now.Sub(l.lastGC) < 5*time.Minute {
		return
	}
	// bucket 補滿所需時間之後即可安全丟棄（等同從未見過）
	idleLimit := time.Duration(l.capacity/l.refill) * time.Second
	if idleLimit < time.Minute {
		idleLimit = time.Minute
	}
	for key, entry := range l.buckets {
		if now.Sub(entry.lastSeen) > idleLimit {
			delete(l.buckets, key)
		}
	}
	l.lastGC = now
}

// clientIP 取出請求來源 IP。
//
// 本服務只透過 Nuxt proxy 對外，因此 X-Forwarded-For 來自我們自己的 proxy，
// 取最後一段（最接近本服務的那一跳）而非第一段，避免客戶端自行偽造前綴繞過限流。
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			parts := splitAndTrim(forwarded, ',')
			if len(parts) > 0 {
				return parts[len(parts)-1]
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func splitAndTrim(value string, sep rune) []string {
	result := make([]string, 0, 2)
	current := make([]rune, 0, len(value))
	flush := func() {
		trimmed := trimSpaceRunes(current)
		if len(trimmed) > 0 {
			result = append(result, string(trimmed))
		}
		current = current[:0]
	}
	for _, char := range value {
		if char == sep {
			flush()
			continue
		}
		current = append(current, char)
	}
	flush()
	return result
}

func trimSpaceRunes(runes []rune) []rune {
	start, end := 0, len(runes)
	for start < end && (runes[start] == ' ' || runes[start] == '\t') {
		start++
	}
	for end > start && (runes[end-1] == ' ' || runes[end-1] == '\t') {
		end--
	}
	return runes[start:end]
}

// withRateLimit 依來源 IP 限流。
func (s *apiServer) withRateLimit(limiter *rateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// preflight 不消耗額度
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		allowed, retryAfter := limiter.allow(clientIP(r, s.trustProxy))
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			writeError(w, http.StatusTooManyRequests, "too many requests; please slow down")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rateLimitAuth 對登入／註冊這類可被暴力破解的端點額外限流。
//
// scope 讓登入與註冊各自計桶：密碼嘗試與帳號建立是不同的濫用型態，
// 共用一個桶會讓正常註冊擠掉登入配額（反之亦然）。
//
// 目前只依 IP 計數。限制：同一 IP 後的多個正常使用者（NAT、辦公室網路）
// 會互相排擠配額。要精準擋住針對單一帳號的暴力破解，需另外依 email 計數；
// 目前主要的防護是 bcrypt 的計算成本。
func (s *apiServer) rateLimitAuth(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			allowed, retryAfter := s.authLimiter.allow(scope + ":" + clientIP(r, s.trustProxy))
			if !allowed {
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				writeError(w, http.StatusTooManyRequests,
					"too many authentication attempts; please try again later")
				return
			}
		}
		next(w, r)
	}
}
