-- 008: Migration 版本表
--
-- 在此之前 migrate.sh 每次部署都重跑所有 *.sql，因此舊的 migration 被迫
-- 預知未來的變更——例如 002 的 CHECK 約束必須含有 005 才引入的狀態值，
-- 否則重跑時會被既有資料列違反。
--
-- 有了版本表之後，每支 migration 只會套用一次，舊檔案不需要知道後續的事。
--
-- 注意：SQL 檔案本身仍應保持 idempotent。整合測試會刻意重跑全部 migration
-- 以建立乾淨的測試 schema，不經過 migrate.sh 的版本記錄。

CREATE TABLE IF NOT EXISTS schema_migrations (
    -- 檔名（不含路徑），例如 002_auth_and_workflow.sql
    version TEXT PRIMARY KEY,
    -- 套用當時的檔案內容雜湊；用於偵測已套用的 migration 被事後修改
    checksum TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE schema_migrations IS
    '已套用的 migration；由 scripts/migrate.sh 維護，請勿手動修改';
