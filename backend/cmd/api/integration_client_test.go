//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
)

// testClient 是帶 cookie jar 的 HTTP 客戶端，行為對應瀏覽器：
// 自動保存 session 與 CSRF cookie，並在變更狀態的請求上附帶 CSRF header。
type testClient struct {
	t      *testing.T
	server *httptest.Server
	http   *http.Client
}

type testResponse struct {
	StatusCode int
	Body       []byte
	Header     http.Header
}

// newTestClient 啟動一個走完整 middleware 鏈的測試伺服器。
func newTestClient(t *testing.T, server *apiServer) *testClient {
	t.Helper()

	httpServer := httptest.NewServer(server.routes())
	t.Cleanup(httpServer.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create cookie jar: %v", err)
	}

	return &testClient{
		t:      t,
		server: httpServer,
		http:   &http.Client{Jar: jar},
	}
}

// fork 回傳共用同一伺服器但持有獨立 cookie jar 的客戶端，
// 用於模擬不同使用者同時操作。
func (c *testClient) fork() *testClient {
	c.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		c.t.Fatalf("create cookie jar: %v", err)
	}
	return &testClient{t: c.t, server: c.server, http: &http.Client{Jar: jar}}
}

func (c *testClient) cookie(name string) string {
	parsed, err := url.Parse(c.server.URL)
	if err != nil {
		return ""
	}
	for _, item := range c.http.Jar.Cookies(parsed) {
		if item.Name == name {
			return item.Value
		}
	}
	return ""
}

// do 發送請求。非安全方法會自動取得並附帶 CSRF token，
// 對應前端 useCsrf().mutate() 的行為。
func (c *testClient) do(method, path string, body any) testResponse {
	c.t.Helper()
	return c.doWithOptions(method, path, body, true)
}

// doWithoutCSRF 刻意不帶 CSRF header，用於驗證防護生效。
func (c *testClient) doWithoutCSRF(method, path string, body any) testResponse {
	c.t.Helper()
	return c.doWithOptions(method, path, body, false)
}

func (c *testClient) doWithOptions(method, path string, body any, withCSRF bool) testResponse {
	c.t.Helper()

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal request body: %v", err)
		}
		payload = bytes.NewReader(encoded)
	}

	if withCSRF && !isSafeMethod(method) && c.cookie(csrfCookieName) == "" {
		// 先取得 token，等同前端首次送出前的行為
		c.doWithOptions(http.MethodGet, "/v1/auth/csrf", nil, false)
	}

	request, err := http.NewRequest(method, c.server.URL+path, payload)
	if err != nil {
		c.t.Fatalf("build request: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if withCSRF && !isSafeMethod(method) {
		if token := c.cookie(csrfCookieName); token != "" {
			request.Header.Set(csrfHeaderName, token)
		}
	}

	response, err := c.http.Do(request)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()

	content, err := io.ReadAll(response.Body)
	if err != nil {
		c.t.Fatalf("read response body: %v", err)
	}
	return testResponse{StatusCode: response.StatusCode, Body: content, Header: response.Header}
}

// expectStatus 斷言狀態碼，失敗時附上回應內容以利診斷。
func (r testResponse) expectStatus(t *testing.T, want int, context string) testResponse {
	t.Helper()
	if r.StatusCode != want {
		t.Fatalf("%s: got HTTP %d, want %d (body: %s)", context, r.StatusCode, want, r.Body)
	}
	return r
}

func (r testResponse) decode(t *testing.T, target any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, target); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, r.Body)
	}
}

// errorMessage 取出後端回傳的 error 欄位。
func (r testResponse) errorMessage(t *testing.T) string {
	t.Helper()
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(r.Body, &payload); err != nil {
		return string(r.Body)
	}
	return payload.Error
}

// ---------------------------------------------------------------- 測試資料 helper

const (
	testPassword         = "test-password-1234"
	testReviewerPassword = "reviewer-password-1234"
)

// createUser 直接寫入資料庫建立使用者，可指定角色與信用評分。
// 註冊 API 只能建立 borrower，reviewer 必須由此建立。
func createUser(t *testing.T, email, displayName, role string, creditScore int) string {
	t.Helper()

	password := testPassword
	if role == roleReviewer {
		password = testReviewerPassword
	}
	hash, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	userID := fmt.Sprintf("usr_test_%s", sanitizeForID(email))
	if _, err := testPool.Exec(testContext(t), `
		INSERT INTO users (id, email, password_hash, display_name, role, credit_score)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE SET
			email = EXCLUDED.email, password_hash = EXCLUDED.password_hash,
			display_name = EXCLUDED.display_name, role = EXCLUDED.role,
			credit_score = EXCLUDED.credit_score
	`, userID, email, hash, displayName, role, creditScore); err != nil {
		t.Fatalf("create user %s: %v", email, err)
	}
	return userID
}

func sanitizeForID(value string) string {
	out := make([]rune, 0, len(value))
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
			out = append(out, char)
		case char >= 'A' && char <= 'Z':
			out = append(out, char+32)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// login 以指定帳密登入，成功後該 client 持有 session。
func (c *testClient) login(email, password string) testResponse {
	c.t.Helper()
	return c.do(http.MethodPost, "/v1/auth/login", map[string]string{
		"email": email, "password": password,
	})
}

// loginAs 登入並斷言成功，回傳登入者資料。
func (c *testClient) loginAs(email, password string) user {
	c.t.Helper()
	response := c.login(email, password).expectStatus(c.t, http.StatusOK, "login "+email)
	var account user
	response.decode(c.t, &account)
	return account
}

// validApplicationBody 回傳一份通過驗證的申請內容。
func validApplicationBody() map[string]any {
	return map[string]any{
		"product":         "個人信用貸款",
		"amount":          600000,
		"termMonths":      36,
		"purpose":         "債務整合",
		"applicantName":   "整合測試",
		"idNumber":        "A123456789",
		"phone":           "0912-345-678",
		"email":           "borrower@creditflow.test",
		"job":             "上市櫃公司員工",
		"employmentYears": "3~5 年",
		"annualIncome":    1200000,
		"monthlyExpenses": 30000,
		"housing":         "租屋",
		"note":            "integration test",
		"documents":       []string{"身分證正反面.jpg"},
	}
}

// submitApplication 送出一筆申請並回傳案件編號。
func (c *testClient) submitApplication() string {
	c.t.Helper()
	response := c.do(http.MethodPost, "/v1/applications", validApplicationBody()).
		expectStatus(c.t, http.StatusCreated, "submit application")
	var created applicationResponse
	response.decode(c.t, &created)
	if created.ID == "" {
		c.t.Fatal("application response has no id")
	}
	return created.ID
}

// approveApplication 以 reviewer 身分核准申請，回傳上架的募資標的。
//
// 核准本身不會生成合約——標的必須募滿才撥款（見 marketplace.go）。
// 需要合約的測試請改用 fundListingFully 或 approveAndDisburse。
func (c *testClient) approveApplication(applicationID string) listingItem {
	c.t.Helper()
	response := c.do(http.MethodPatch, "/v1/admin/applications/"+applicationID,
		map[string]string{"action": "approve", "reason": "integration test"}).
		expectStatus(c.t, http.StatusOK, "approve "+applicationID)

	var payload struct {
		Listing listingItem `json:"listing"`
	}
	response.decode(c.t, &payload)
	if payload.Listing.ID == "" {
		c.t.Fatal("approval did not create a funding listing")
	}
	return payload.Listing
}

// fundListingFully 以一位出借人一次投滿標的，觸發撥款並回傳生成的合約。
func (c *testClient) fundListingFully(listingID string, amount int64) loan {
	c.t.Helper()
	response := c.do(http.MethodPost, "/v1/listings/"+listingID+"/invest",
		map[string]any{"amount": amount, "requestId": "fund-fully"}).
		expectStatus(c.t, http.StatusCreated, "fund listing "+listingID)

	var payload investResponse
	response.decode(c.t, &payload)
	if payload.DisbursedLoa == nil {
		c.t.Fatalf("funding %s did not disburse a loan", listingID)
	}
	return *payload.DisbursedLoa
}

// approveAndDisburse 走完整流程：送申請 → 核准上架 → 出借人投滿 → 撥款。
//
// 需要「已撥款合約」的測試用這個 helper，而不是各自重複整套流程。
// investorEmail 必須是已建立且餘額足夠的出借人。
func approveAndDisburse(t *testing.T, borrower, reviewer *testClient, investorEmail string) loan {
	t.Helper()

	applicationID := borrower.submitApplication()
	created := reviewer.approveApplication(applicationID)

	investor := borrower.fork()
	investor.loginAs(investorEmail, testInvestorPassword)
	return investor.fundListingFully(created.ID, created.TargetAmount)
}

// upload 以 multipart 送出檔案，並自動附帶 CSRF token。
func (c *testClient) upload(path, fieldName, fileName string, content []byte) testResponse {
	c.t.Helper()

	if c.cookie(csrfCookieName) == "" {
		c.doWithOptions(http.MethodGet, "/v1/auth/csrf", nil, false)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(fieldName, fileName)
	if err != nil {
		c.t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		c.t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		c.t.Fatalf("close multipart writer: %v", err)
	}

	request, err := http.NewRequest(http.MethodPost, c.server.URL+path, &body)
	if err != nil {
		c.t.Fatalf("build upload request: %v", err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if token := c.cookie(csrfCookieName); token != "" {
		request.Header.Set(csrfHeaderName, token)
	}

	response, err := c.http.Do(request)
	if err != nil {
		c.t.Fatalf("upload %s: %v", path, err)
	}
	defer response.Body.Close()

	payload, err := io.ReadAll(response.Body)
	if err != nil {
		c.t.Fatalf("read upload response: %v", err)
	}
	return testResponse{StatusCode: response.StatusCode, Body: payload, Header: response.Header}
}

// download 取得檔案內容（不解析成 JSON）。
func (c *testClient) download(path string) testResponse {
	c.t.Helper()
	return c.doWithOptions(http.MethodGet, path, nil, false)
}

// buildMultipartRequest 組出 multipart 請求但不附帶 CSRF header，
// 用於驗證 CSRF 防護對檔案上傳同樣生效。
func buildMultipartRequest(
	t *testing.T, url, fieldName, fileName string, content []byte,
) (*http.Request, *bytes.Buffer) {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(fieldName, fileName)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	request, err := http.NewRequest(http.MethodPost, url, &body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request, &body
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	return parsed
}

// decodeApplications 解析分頁後的申請清單，回傳項目與分頁資訊。
//
// 清單端點改為分頁後回傳 {items, page} 而非裸陣列；
// 測試多半只關心項目，因此以 helper 隱藏這層結構。
func (r testResponse) decodeApplications(t *testing.T) ([]applicationSummary, pageMeta) {
	t.Helper()
	var payload paginatedApplications
	r.decode(t, &payload)
	return payload.Items, payload.Page
}
