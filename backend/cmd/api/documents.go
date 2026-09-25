package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type documentRecord struct {
	ID           string    `json:"id"`
	OriginalName string    `json:"originalName"`
	ContentType  string    `json:"contentType"`
	SizeBytes    int64     `json:"sizeBytes"`
	Checksum     string    `json:"checksum"`
	CreatedAt    time.Time `json:"createdAt"`

	// OCR 擷取狀態與比對結果。
	// 注意：OCR 不判斷文件真偽，結果僅供風控參考。
	OCRStatus string `json:"ocrStatus"`
	OCRError  string `json:"ocrError,omitempty"`
	// 各項目的比對結果：matched / not_found / unreadable
	Verifications map[string]string `json:"verifications,omitempty"`
}

// 申請在這些狀態下仍可增刪文件。
// 已進入募資或已決議的案件不可再變更附件，否則審核依據會事後被改動。
var documentEditableStatuses = map[string]bool{
	"pending":            true,
	"reviewing":          true,
	"more_info_required": true,
}

// applicationAccess 描述呼叫者對某份申請的權限。
type applicationAccess struct {
	OwnerID  string
	Status   string
	CanRead  bool
	CanWrite bool
}

// checkApplicationAccess 判斷呼叫者能否讀寫該申請的文件。
//
// 借款人只能碰自己的申請；reviewer 可讀所有申請（審核需要）但不可增刪文件。
func (s *apiServer) checkApplicationAccess(
	ctx context.Context, applicationID string, actor *user,
) (applicationAccess, error) {
	var access applicationAccess
	var ownerID *string

	err := s.db.QueryRow(ctx,
		`SELECT user_id, status FROM applications WHERE id = $1`,
		applicationID).Scan(&ownerID, &access.Status)
	if err != nil {
		return access, err
	}
	if ownerID != nil {
		access.OwnerID = *ownerID
	}

	switch actor.Role {
	case roleReviewer:
		access.CanRead = true
		access.CanWrite = false
	default:
		isOwner := access.OwnerID != "" && access.OwnerID == actor.ID
		access.CanRead = isOwner
		access.CanWrite = isOwner && documentEditableStatuses[access.Status]
	}
	return access, nil
}

// ---------------------------------------------------------------- 上傳

// uploadDocument 接收 multipart 檔案上傳。
// 路由：POST /v1/applications/{id}/documents
func (s *apiServer) uploadDocument(w http.ResponseWriter, r *http.Request, applicationID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor := currentUser(r)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	access, err := s.checkApplicationAccess(ctx, applicationID, actor)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "application not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load application")
		return
	}
	// 非擁有者一律回 404，不洩漏該申請是否存在
	if !access.CanRead {
		writeError(w, http.StatusNotFound, "application not found")
		return
	}
	if !access.CanWrite {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("documents cannot be changed while the application is %s", access.Status))
		return
	}

	// 先限制請求大小，避免在解析前就吃掉大量記憶體
	r.Body = http.MaxBytesReader(w, r.Body, maxDocumentBytes+1<<20)
	if err := r.ParseMultipartForm(maxDocumentBytes + 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart form or file too large")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "a file field is required")
		return
	}
	defer file.Close()

	if header.Size > maxDocumentBytes {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("file exceeds the %d MiB limit", maxDocumentBytes>>20))
		return
	}

	// 多讀 1 byte 以偵測超出上限的情況（header.Size 可能不準）
	content, err := io.ReadAll(io.LimitReader(file, maxDocumentBytes+1))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read the uploaded file")
		return
	}
	if len(content) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "the uploaded file is empty")
		return
	}
	if int64(len(content)) > maxDocumentBytes {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("file exceeds the %d MiB limit", maxDocumentBytes>>20))
		return
	}

	// 以 magic bytes 判定型別：副檔名與 Content-Type 都可偽造
	contentType, err := detectContentType(content)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity,
			"only PDF, JPEG and PNG files are accepted")
		return
	}

	var existing int
	if err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM application_documents WHERE application_id = $1`,
		applicationID).Scan(&existing); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count documents")
		return
	}
	if existing >= maxDocumentsPerApp {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("an application may hold at most %d documents", maxDocumentsPerApp))
		return
	}

	documentID, err := newDocumentID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create document id")
		return
	}
	storagePath, err := generatePath(contentType)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to allocate storage")
		return
	}

	// 先寫檔再寫資料庫：若資料庫失敗則刪檔，避免留下無主檔案。
	// 反之若先寫資料庫，程序當掉會留下指向不存在檔案的紀錄。
	if err := s.storage.write(storagePath, content); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store the document")
		return
	}

	record := documentRecord{
		ID:           documentID,
		OriginalName: sanitizeOriginalName(header.Filename),
		ContentType:  contentType,
		SizeBytes:    int64(len(content)),
		Checksum:     checksumOf(content),
		// OCR 在背景進行，不阻塞上傳回應
		OCRStatus: "pending",
	}

	err = s.db.QueryRow(ctx, `
		INSERT INTO application_documents (
			id, application_id, uploaded_by, original_name,
			storage_path, content_type, size_bytes, checksum
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING created_at
	`, documentID, applicationID, actor.ID, record.OriginalName, storagePath,
		contentType, record.SizeBytes, record.Checksum).Scan(&record.CreatedAt)
	if err != nil {
		_ = s.storage.remove(storagePath)
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "this file was already uploaded to this application")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to record the document")
		return
	}

	writeJSON(w, http.StatusCreated, record)
}

// ---------------------------------------------------------------- 清單

func (s *apiServer) listDocuments(w http.ResponseWriter, r *http.Request, applicationID string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor := currentUser(r)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	access, err := s.checkApplicationAccess(ctx, applicationID, actor)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "application not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load application")
		return
	}
	if !access.CanRead {
		writeError(w, http.StatusNotFound, "application not found")
		return
	}

	rows, err := s.db.Query(ctx, `
		SELECT d.id, d.original_name, d.content_type, d.size_bytes, d.checksum,
		       d.created_at, d.ocr_status, d.ocr_error,
		       COALESCE(
		         (SELECT jsonb_object_agg(v.field, v.result)
		          FROM document_verifications v WHERE v.document_id = d.id),
		         '{}'::jsonb
		       )
		FROM application_documents d
		WHERE d.application_id = $1
		ORDER BY d.created_at, d.id
	`, applicationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load documents")
		return
	}
	defer rows.Close()

	items := make([]documentRecord, 0)
	for rows.Next() {
		var item documentRecord
		if err := rows.Scan(&item.ID, &item.OriginalName, &item.ContentType,
			&item.SizeBytes, &item.Checksum, &item.CreatedAt,
			&item.OCRStatus, &item.OCRError, &item.Verifications); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to decode documents")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read documents")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// ---------------------------------------------------------------- 下載與刪除

// documentRoutes 分派 /v1/documents/{id}... 的請求。
func (s *apiServer) documentRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/documents/"), "/")
	documentID, suffix, hasSuffix := strings.Cut(rest, "/")
	if documentID == "" {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}

	switch {
	case hasSuffix && suffix == "download":
		s.downloadDocument(w, r, documentID)
	case !hasSuffix && r.Method == http.MethodDelete:
		s.deleteDocument(w, r, documentID)
	default:
		writeError(w, http.StatusNotFound, "route not found")
	}
}

// loadDocumentForActor 取出文件並確認呼叫者有權存取。
func (s *apiServer) loadDocumentForActor(
	ctx context.Context, documentID string, actor *user,
) (applicationID, storagePath, originalName, contentType string, access applicationAccess, err error) {
	err = s.db.QueryRow(ctx, `
		SELECT application_id, storage_path, original_name, content_type
		FROM application_documents WHERE id = $1
	`, documentID).Scan(&applicationID, &storagePath, &originalName, &contentType)
	if err != nil {
		return
	}
	access, err = s.checkApplicationAccess(ctx, applicationID, actor)
	return
}

func (s *apiServer) downloadDocument(w http.ResponseWriter, r *http.Request, documentID string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor := currentUser(r)

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	_, storagePath, originalName, contentType, access, err := s.loadDocumentForActor(ctx, documentID, actor)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "document not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load document")
		return
	}
	if !access.CanRead {
		writeError(w, http.StatusNotFound, "document not found")
		return
	}

	file, err := s.storage.open(storagePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read the stored document")
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", contentType)
	// attachment 避免瀏覽器內嵌執行；檔名以 RFC 5987 編碼以支援非 ASCII
	w.Header().Set("Content-Disposition", contentDispositionFor(originalName))
	// 明確禁止嗅探型別，降低把使用者上傳內容當成其他型別執行的風險
	w.Header().Set("X-Content-Type-Options", "nosniff")

	http.ServeContent(w, r, originalName, time.Time{}, file)
}

func (s *apiServer) deleteDocument(w http.ResponseWriter, r *http.Request, documentID string) {
	actor := currentUser(r)

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	_, storagePath, _, _, access, err := s.loadDocumentForActor(ctx, documentID, actor)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "document not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load document")
		return
	}
	if !access.CanRead {
		writeError(w, http.StatusNotFound, "document not found")
		return
	}
	if !access.CanWrite {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("documents cannot be changed while the application is %s", access.Status))
		return
	}

	if _, err := s.db.Exec(ctx,
		`DELETE FROM application_documents WHERE id = $1`, documentID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete the document")
		return
	}
	// 先刪紀錄再刪檔：刪檔失敗只會留下無主檔案（可由清理工作處理），
	// 反之則會留下指向不存在檔案的紀錄，使用者會看到壞掉的下載連結。
	if err := s.storage.remove(storagePath); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "deleted",
			"note":   "資料庫紀錄已移除，但儲存的檔案未能刪除",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// contentDispositionFor 組出可安全放進 HTTP 標頭的 Content-Disposition。
//
// HTTP 標頭只能放 ASCII，中文等字元直接寫入會變成亂碼。
// 依 RFC 6266／5987 同時提供兩種形式：
//   - filename=   僅 ASCII，供舊瀏覽器 fallback
//   - filename*=  UTF-8 百分比編碼，現代瀏覽器優先採用
func contentDispositionFor(name string) string {
	ascii := asciiFallbackName(name)
	return fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s",
		ascii, url.PathEscape(name))
}

// asciiFallbackName 產生純 ASCII 的替代檔名。
// 非 ASCII 字元以底線取代；若整個名稱都被取代則退回通用名稱。
func asciiFallbackName(name string) string {
	builder := make([]rune, 0, len(name))
	meaningful := false
	for _, char := range name {
		switch {
		case char > 127:
			builder = append(builder, '_')
		case char == '"' || char == '\\':
			builder = append(builder, '_')
		default:
			builder = append(builder, char)
			if char != '_' && char != '.' && char != ' ' {
				meaningful = true
			}
		}
	}
	if !meaningful {
		// 保留副檔名，讓 fallback 仍看得出型別
		if index := strings.LastIndex(name, "."); index >= 0 && index < len(name)-1 {
			return "document" + name[index:]
		}
		return "document"
	}
	return string(builder)
}
