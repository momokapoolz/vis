# Kiến trúc và quy ước ledger

Go HTTP monolith → services dùng SQL trực tiếp → PostgreSQL. Migrations chạy bằng role riêng.
Không có repository interface, ORM, Redis hoặc background queue.

```mermaid
flowchart LR
    Client[Customer / Staff] --> HTTP[Go net/http API]
    Mock[CLI mock provider] -->|HMAC raw JSON| HTTP
    HTTP --> Auth[JWT + DB role/ownership]
    Auth --> Service[Wallet / Ledger / Reconciliation]
    Service --> DB[(PostgreSQL)]
    Scheduler[Windows Task Scheduler] --> CLI[wallet reconcile]
    CLI --> Service
    Migrate[wallet migrate: owner role] --> DB
```

## ERD

```mermaid
erDiagram
    users ||--o{ wallets : owns
    users ||--o{ refresh_tokens : authenticates
    users ||--o{ idempotency_keys : scopes
    users o|--o{ transactions : performs
    users o|--o{ reconciliation_runs : requests
    wallets ||--o{ ledger_entries : records
    transactions ||--|{ ledger_entries : contains
    transactions o|--o| transactions : reverses
    users {
        bigint id PK
        text email UK
        text password_digest
        text role
    }
    wallets {
        bigint id PK
        bigint user_id FK "nullable only for system"
        text kind
        text currency "VND"
        text status
    }
    transactions {
        bigint id PK
        text transaction_type
        text status "posted"
        bigint reversed_transaction_id FK,UK
        bigint actor_user_id FK
        text reference_id
        text reason
    }
    ledger_entries {
        bigint id PK
        bigint transaction_id FK
        bigint wallet_id FK
        text entry_type
        bigint amount
    }
    idempotency_keys {
        bigint id PK
        bigint user_id FK
        text key "unique with user_id"
        text request_hash
        text status
        int response_code
        bytea response_body
    }
    webhook_events {
        bigint id PK
        text provider_event_id UK
        jsonb payload
        text request_hash
        text status
        bytea response_body
    }
    refresh_tokens {
        bigint id PK
        bigint user_id FK
        text token_digest UK
        timestamptz expires_at
        timestamptz revoked_at
    }
    reconciliation_runs {
        bigint id PK
        bigint actor_user_id FK
        text status
        jsonb report
    }
```

Webhook event ID được ghi vào `transactions.reference_id` để truy vết; event và giao dịch commit cùng nhau.
Mỗi transaction có đúng hai entries, được deferred constraint trigger kiểm tra tại commit.
Unique `(transaction_id, entry_type)` và `(transaction_id, wallet_id)` bảo đảm hai phía và hai ví khác nhau.

## Debit/credit

Quy ước ví của PoC: **credit tăng, debit giảm**, balance = credits − debits.
Đây là ledger cho số dư ví mô phỏng, chưa phải một chart of accounts đầy đủ cho doanh nghiệp.

- Deposit: debit Provider Clearing, credit ví khách hàng.
- Transfer: debit source, credit destination.
- Reversal: đảo debit/credit của giao dịch gốc, giữ nguyên amount và liên kết bằng `reversed_transaction_id`.
- Chỉ clearing được âm; kiểm tra không âm và overflow nằm trong service chung sau khi khóa ví.

## Transfer và commit

```mermaid
sequenceDiagram
    participant C as Client
    participant A as API
    participant D as PostgreSQL
    C->>A: POST /transfers + JWT + Idempotency-Key
    A->>A: Parse, authenticate, authorize, validate
    A->>D: BEGIN READ COMMITTED
    A->>D: INSERT key ON CONFLICT DO NOTHING
    alt Key đã hoàn thành
        A->>D: Read hash + stored response
        A->>D: ROLLBACK read-only replay transaction
        A-->>C: Replay hoặc 409 nếu hash khác
    else Key mới
        A->>D: SELECT wallets ORDER BY id FOR UPDATE
        A->>D: Separate SELECT SUM(entries) after all locks
        alt Thiếu tiền hoặc vi phạm nghiệp vụ
            A->>D: Save error response and COMMIT
            A-->>C: 409
        else Hợp lệ
            A->>D: INSERT transaction, debit, credit
            A->>D: Save response on key
            A->>D: COMMIT (deferred balance constraint)
            A-->>C: 201
        end
    end
```

Không tính balance trước lock hoặc trong cùng statement đang chờ lock.
Tất cả luồng ghi tiền khóa ví theo ID tăng dần. Reversal khóa giao dịch gốc trước các ví;
unique `reversed_transaction_id` bổ sung bảo vệ chống hoàn hai lần.
Reconciliation dùng một SQL statement cho tất cả phép kiểm tra để tránh trộn snapshot.

Key/event và ledger cùng commit nên mất response sau commit không tạo lần ghi thứ hai khi retry.
Lỗi DB rollback cả key/event lẫn entries. Lỗi nghiệp vụ được replay, không tự chạy lại sau khi số dư thay đổi.
Không gọi provider/network trong transaction.

## Bảo vệ và giới hạn

Role ứng dụng có SELECT/INSERT cần thiết và UPDATE giới hạn cho token/idempotency/event.
UPDATE riêng cột `id` của wallets/transactions cho phép PostgreSQL thực hiện `SELECT FOR UPDATE`;
API không có đường cập nhật ID. Trigger chặn sửa/xóa ledger và transactions kể cả qua tài khoản owner thông thường.
Superuser có thể bypass trigger; chỉ suite test dùng khả năng này trong database riêng để tạo anomaly.

`post` là đường ghi sổ duy nhất trong ứng dụng. Kiểm tra không âm dựa vào mọi caller tuân thủ luồng khóa này;
không coi truy cập SQL đặc quyền tùy ý là một API ghi tiền được hỗ trợ.
Chưa triển khai cleanup idempotency, balance snapshots hoặc phân vùng ledger.
