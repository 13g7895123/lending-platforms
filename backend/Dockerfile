FROM golang:1.22-alpine AS build

WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/creditflow-api ./cmd/api

FROM alpine:3.21

# tesseract 供文件 OCR；chi_tra 用於身分證與薪轉等中文文件，
# poppler-utils 的 pdftoppm 把 PDF 轉為圖片後才能辨識。
# 缺少這些工具時 API 仍可啟動，文件會標記為 skipped（見 startOCRWorker）。
RUN apk add --no-cache \
    ca-certificates wget \
    tesseract-ocr tesseract-ocr-data-eng tesseract-ocr-data-chi_tra \
    poppler-utils
WORKDIR /app
COPY --from=build /out/creditflow-api ./creditflow-api
COPY backend/migrations ./migrations
COPY backend/seed ./seed

# 文件儲存目錄需在映像中先建立並歸屬給執行身分。
# Docker 掛載具名 volume 時會沿用掛載點在映像中的擁有者與權限；
# 若映像中不存在該路徑，volume 會以 root 建立，非 root 的程序就寫不進去。
ENV DOCUMENT_STORAGE_ROOT=/var/lib/creditflow/documents
RUN mkdir -p "$DOCUMENT_STORAGE_ROOT" \
    && chown -R nobody:nobody "$DOCUMENT_STORAGE_ROOT"

ENV PORT=8080
EXPOSE 8080
USER nobody
ENTRYPOINT ["/app/creditflow-api"]
