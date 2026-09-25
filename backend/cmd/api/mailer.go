package main

import (
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// 寄信用於 email 驗證與密碼重設。
//
// 未設定 SMTP_HOST 時不寄信，改把內容寫進日誌：
// 本機開發不該因為缺少郵件伺服器而無法測試整個流程。
// develop 環境的 compose 會啟動 MailHog，因此預設就有可用的收件端。

type mailer struct {
	host string
	port string
	from string
	// 寄信逾時。SMTP 連線卡住不該讓 HTTP 請求一起卡住。
	timeout time.Duration
	logger  *slog.Logger
}

func newMailer(logger *slog.Logger) *mailer {
	return &mailer{
		host:    strings.TrimSpace(env("SMTP_HOST", "")),
		port:    env("SMTP_PORT", "1025"),
		from:    env("MAIL_FROM", "no-reply@creditflow.test"),
		timeout: time.Duration(envInt("SMTP_TIMEOUT_SECONDS", 10)) * time.Second,
		logger:  logger,
	}
}

// enabled 回報是否真的會寄出信件。
func (m *mailer) enabled() bool {
	return m.host != ""
}

// send 寄出一封純文字信件。
//
// 刻意不回傳錯誤給呼叫端的 HTTP 回應：寄信失敗不該讓註冊或重設請求失敗
// （使用者可以重寄），但必須留下日誌以便排查。
func (m *mailer) send(to, subject, body string) {
	if !m.enabled() {
		// 開發環境的替代路徑：把信件內容寫進日誌，流程仍可完整測試
		m.logger.Info("email not sent (SMTP_HOST is unset); logging instead",
			"to", to, "subject", subject, "body", body)
		return
	}

	address := net.JoinHostPort(m.host, m.port)
	message := m.compose(to, subject, body)

	// 自行建立連線以套用逾時；smtp.SendMail 沒有逾時參數
	connection, err := net.DialTimeout("tcp", address, m.timeout)
	if err != nil {
		m.logger.Error("could not reach the mail server", "address", address, "error", err)
		return
	}
	defer func() { _ = connection.Close() }()

	if err := connection.SetDeadline(time.Now().Add(m.timeout)); err != nil {
		m.logger.Error("could not set a mail deadline", "error", err)
		return
	}

	client, err := smtp.NewClient(connection, m.host)
	if err != nil {
		m.logger.Error("could not start an SMTP session", "error", err)
		return
	}
	defer func() { _ = client.Close() }()

	if err := client.Mail(m.from); err != nil {
		m.logger.Error("SMTP sender was rejected", "from", m.from, "error", err)
		return
	}
	if err := client.Rcpt(to); err != nil {
		m.logger.Error("SMTP recipient was rejected", "to", to, "error", err)
		return
	}

	writer, err := client.Data()
	if err != nil {
		m.logger.Error("could not open the SMTP data stream", "error", err)
		return
	}
	if _, err := writer.Write([]byte(message)); err != nil {
		m.logger.Error("could not write the message body", "error", err)
		return
	}
	if err := writer.Close(); err != nil {
		m.logger.Error("could not finish the message", "error", err)
		return
	}
	_ = client.Quit()

	m.logger.Info("email sent", "to", to, "subject", subject)
}

// compose 組出 RFC 5322 格式的信件。
//
// 標頭值會先清掉換行：主旨或收件人若含 CR/LF 會讓攻擊者注入額外標頭
// （header injection），進而夾帶其他收件人。
func (m *mailer) compose(to, subject, body string) string {
	var builder strings.Builder
	builder.WriteString("From: " + sanitizeHeaderValue(m.from) + "\r\n")
	builder.WriteString("To: " + sanitizeHeaderValue(to) + "\r\n")
	builder.WriteString("Subject: " + sanitizeHeaderValue(subject) + "\r\n")
	builder.WriteString("MIME-Version: 1.0\r\n")
	builder.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	builder.WriteString("\r\n")
	// 主體中的單獨 CR 或 LF 統一為 CRLF
	builder.WriteString(normalizeLineEndings(body))
	return builder.String()
}

// sanitizeHeaderValue 移除會造成標頭注入的字元。
func sanitizeHeaderValue(value string) string {
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	return strings.TrimSpace(value)
}

func normalizeLineEndings(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	return strings.ReplaceAll(body, "\n", "\r\n")
}

// ---------------------------------------------------------------- 信件內容

// verificationEmail 組出驗證信的主旨與內容。
func verificationEmail(baseURL, displayName, token string) (string, string) {
	link := fmt.Sprintf("%s/verify-email?token=%s", strings.TrimRight(baseURL, "/"), token)
	body := fmt.Sprintf(`%s 您好，

請點擊以下連結完成 CreditFlow 帳號的電子信箱驗證：

%s

此連結將於 24 小時後失效。若您並未註冊 CreditFlow，請忽略本信。

CreditFlow 信達金融`, displayName, link)
	return "請驗證您的 CreditFlow 電子信箱", body
}

// resetPasswordEmail 組出密碼重設信的主旨與內容。
func resetPasswordEmail(baseURL, displayName, token string) (string, string) {
	link := fmt.Sprintf("%s/reset-password?token=%s", strings.TrimRight(baseURL, "/"), token)
	body := fmt.Sprintf(`%s 您好，

我們收到重設 CreditFlow 帳號密碼的請求。請點擊以下連結設定新密碼：

%s

此連結將於 1 小時後失效，且僅能使用一次。
重設完成後，所有裝置上的登入狀態都會被登出。

若您並未要求重設密碼，請忽略本信，您的密碼不會有任何變更。

CreditFlow 信達金融`, displayName, link)
	return "重設您的 CreditFlow 密碼", body
}
